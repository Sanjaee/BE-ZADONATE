package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// Hub maintains the set of active clients and broadcasts messages to the clients
type Hub struct {
	clients         map[*Client]bool
	broadcast       chan []byte
	register        chan *Client
	unregister      chan *Client
	mu              sync.RWMutex
	pendingDonation []byte // Store last donation message for reconnection
	pendingMedia    []byte // Store last media message for reconnection
}

// Client is a middleman between the websocket connection and the hub
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// DonationMessage represents a donation notification
type DonationMessage struct {
	ID         string `json:"id,omitempty"` // UUID for tracking this donation
	Type       string `json:"type"`         // "donation", "media", "visibility", "time", "gif", "text", "history", "payment_status"
	DonorName  string `json:"donorName,omitempty"`
	Amount     int    `json:"amount,omitempty"` // Integer amount
	Message    string `json:"message,omitempty"`
	MediaURL   string `json:"mediaUrl,omitempty"`
	MediaType  string `json:"mediaType,omitempty"` // "image", "video", "youtube", "instagram", or "tiktok"
	StartTime  int    `json:"startTime,omitempty"` // Start time in seconds for YouTube videos (legacy)
	Duration   int    `json:"duration,omitempty"`  // Display duration in milliseconds
	Visible    bool   `json:"visible,omitempty"`
	TargetTime string `json:"targetTime,omitempty"` // For time countdown: "YYYY-MM-DDTHH:mm:ss" OR for YouTube start time: seconds (as string or int)
	CreatedAt  string `json:"createdAt,omitempty"`  // For history: creation timestamp
	// Payment status fields
	PaymentID    string `json:"paymentId,omitempty"`
	OrderID      string `json:"orderId,omitempty"`
	Status       string `json:"status,omitempty"` // PENDING, SUCCESS, FAILED, etc
	VANumber     string `json:"vaNumber,omitempty"`
	BankType     string `json:"bankType,omitempty"`
	QRCodeURL    string `json:"qrCodeUrl,omitempty"`
	ExpiryTime   string `json:"expiryTime,omitempty"`
	DonationType string `json:"donationType,omitempty"`
}

var hub = &Hub{
	clients:    make(map[*Client]bool),
	broadcast:  make(chan []byte),
	register:   make(chan *Client),
	unregister: make(chan *Client),
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			count := len(h.clients)
			// Send pending messages to newly connected client
			if h.pendingDonation != nil {
				select {
				case client.send <- h.pendingDonation:
					log.Printf("📤 Sent pending donation to newly connected client")
				default:
					// Channel full, skip
				}
			}
			if h.pendingMedia != nil {
				select {
				case client.send <- h.pendingMedia:
					log.Printf("📤 Sent pending media to newly connected client")
				default:
					// Channel full, skip
				}
			}
			h.mu.Unlock()
			log.Printf("✅ WebSocket client connected. Total clients: %d", count)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("🔌 WebSocket client disconnected. Total clients: %d", count)

		case message := <-h.broadcast:
			h.mu.RLock()
			clients := make([]*Client, 0, len(h.clients))
			for client := range h.clients {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			// Parse message to determine type and store if needed
			var msgType string
			var msgMap map[string]interface{}
			if err := json.Unmarshal(message, &msgMap); err == nil {
				if t, ok := msgMap["type"].(string); ok {
					msgType = t
					// Store donation and media messages for reconnection
					if msgType == "donation" || msgType == "text" {
						h.mu.Lock()
						h.pendingDonation = message
						h.mu.Unlock()
					} else if msgType == "media" {
						h.mu.Lock()
						h.pendingMedia = message
						h.mu.Unlock()
					}
				}
			}

			if len(clients) == 0 {
				log.Printf("⚠️  No WebSocket clients connected to receive message (type: %s), stored for reconnection", msgType)
			} else {
				log.Printf("📤 Broadcasting message to %d client(s) (type: %s)", len(clients), msgType)
			}

			for _, client := range clients {
				select {
				case client.send <- message:
					// Message sent successfully
				default:
					// Client send channel is full or closed
					log.Printf("⚠️  WebSocket client send channel full, removing client")
					h.mu.Lock()
					close(client.send)
					delete(h.clients, client)
					h.mu.Unlock()
				}
			}
		}
	}
}

// readPump pumps messages from the websocket connection to the hub
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

// writePump pumps messages from the hub to the websocket connection
func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Send each message separately
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		case <-ticker.C:
			// Send ping to keep connection alive
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("WebSocket ping error: %v", err)
				return
			}
		}
	}
}

// ServeWS handles websocket requests from clients
func ServeWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	client.hub.register <- client

	go client.writePump()
	go client.readPump()
}

// BroadcastDonation sends a donation message to all connected clients (auto-visible)
func BroadcastDonation(id, donorName string, amount int, message string, durationMs int) {
	msg := DonationMessage{
		ID:        id,
		Type:      "donation",
		DonorName: donorName,
		Amount:    amount,
		Message:   message,
		Duration:  durationMs,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling donation message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted donation: %s - %d (ID: %s, duration: %dms)", donorName, amount, id, durationMs)
}

// BroadcastMedia sends a media update to all connected clients (auto-visible)
func BroadcastMedia(id, mediaURL, mediaType string, startTime int) {
	msg := DonationMessage{
		ID:        id,
		Type:      "media",
		MediaURL:  mediaURL,
		MediaType: mediaType,
		StartTime: startTime,
		// Also send as targetTime string for frontend compatibility
		TargetTime: strconv.Itoa(startTime),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling media message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted media: %s (%s) startTime: %d (ID: %s)", mediaURL, mediaType, startTime, id)
}

// BroadcastVisibility sends a visibility update to all connected clients
func BroadcastVisibility(id string, visible bool) {
	msg := DonationMessage{
		ID:      id,
		Type:    "visibility",
		Visible: visible,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling visibility message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted visibility: %v (ID: %s)", visible, id)
}

// BroadcastTime sends a time countdown target to all connected clients
func BroadcastTime(targetTime string) {
	msg := DonationMessage{
		Type:       "time",
		TargetTime: targetTime,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling time message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted time target: %s", targetTime)
}

// BroadcastHistory sends a new donation history to all connected clients
func BroadcastHistory(history *DonationHistory) {
	message := DonationMessage{
		Type:      "history",
		ID:        history.ID,
		DonorName: history.DonorName,
		Amount:    history.Amount,
		Message:   history.Message,
		MediaURL:  history.MediaURL,
		MediaType: history.MediaType,
		StartTime: history.StartTime,
		CreatedAt: history.CreatedAt.Format(time.RFC3339),
	}

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling history message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("📤 Broadcasted history: %s - %s - Rp%d", history.ID, history.DonorName, history.Amount)
}

// BroadcastPaymentStatus sends payment status update to all connected clients
func BroadcastPaymentStatus(payment *Payment) {
	message := DonationMessage{
		Type:         "payment_status",
		PaymentID:    payment.ID,
		OrderID:      payment.OrderID,
		Status:       string(payment.Status),
		VANumber:     payment.VANumber,
		BankType:     payment.BankType,
		QRCodeURL:    payment.QRCodeURL,
		DonorName:    payment.DonorName,
		Amount:       payment.Amount,
		DonationType: payment.DonationType,
		Message:      payment.Message,
		MediaURL:     payment.MediaURL,
		MediaType:    payment.MediaType,
		StartTime:    payment.StartTime,
	}

	if payment.ExpiryTime != nil {
		message.ExpiryTime = payment.ExpiryTime.Format(time.RFC3339)
	}

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling payment status message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("📤 Broadcasted payment status: %s - %s - %s", payment.OrderID, payment.DonorName, payment.Status)
}

// BroadcastText sends a text-only donation message to all connected clients
func BroadcastText(id, donorName string, amount int, message string, durationMs int) {
	msg := DonationMessage{
		ID:        id,
		Type:      "text",
		DonorName: donorName,
		Amount:    amount,
		Message:   message,
		Duration:  durationMs,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling text message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted text: %s - %d (ID: %s, duration: %dms)", donorName, amount, id, durationMs)
}
