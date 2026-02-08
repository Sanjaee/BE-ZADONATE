package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// remainingMs is sent to newly connected clients so they show current donation with correct remaining time (no "replay" from start).
const remainingMsKey = "remainingMs"

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// Hub maintains the set of active clients and broadcasts messages to the clients.
// pendingDonation/pendingMedia are only "currently playing"; cleared when donation ends so new clients don't see old donation.
type Hub struct {
	clients              map[*Client]bool
	broadcast            chan []byte
	register             chan *Client
	unregister           chan *Client
	mu                   sync.RWMutex
	pendingDonation      []byte    // Current donation message (cleared when visibility false)
	pendingMedia         []byte    // Current media message (cleared when visibility false)
	currentDisplayEndsAt time.Time // When current donation display ends; zero = nothing showing
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
	PaymentID     string `json:"paymentId,omitempty"`
	OrderID       string `json:"orderId,omitempty"`
	Status        string `json:"status,omitempty"` // PENDING, SUCCESS, FAILED, etc
	VANumber      string `json:"vaNumber,omitempty"`
	BankType      string `json:"bankType,omitempty"`
	QRCodeURL     string `json:"qrCodeUrl,omitempty"`
	ExpiryTime    string `json:"expiryTime,omitempty"`
	DonationType  string `json:"donationType,omitempty"`
	PaymentMethod string `json:"paymentMethod,omitempty"` // crypto, bank_transfer, gopay, etc
	PaymentType   string `json:"paymentType,omitempty"`   // plisio, midtrans
	// Crypto payment fields
	PlisioCurrency string `json:"plisioCurrency,omitempty"` // BTC, ETH, SOL, etc
	PlisioAmount   string `json:"plisioAmount,omitempty"`   // Crypto amount (e.g., "0.001", "0.5")
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
			// Only send current donation/media if one is still playing (display runs in background; web only displays)
			now := time.Now()
			if !h.currentDisplayEndsAt.IsZero() && h.currentDisplayEndsAt.After(now) && (h.pendingDonation != nil || h.pendingMedia != nil) {
				remainingMs := int(time.Until(h.currentDisplayEndsAt).Milliseconds())
				if remainingMs > 0 {
					// Send media first, then donation with remainingMs so FE shows correct remaining time
					if h.pendingMedia != nil {
						select {
						case client.send <- h.pendingMedia:
						default:
						}
					}
					if h.pendingDonation != nil {
						donationWithRemaining := injectRemainingMs(h.pendingDonation, remainingMs)
						if donationWithRemaining != nil {
							select {
							case client.send <- donationWithRemaining:
							default:
							}
						}
					}
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

			// Parse message to determine type and store if needed (only "currently playing" state)
			var msgType string
			var msgMap map[string]interface{}
			if err := json.Unmarshal(message, &msgMap); err == nil {
				if t, ok := msgMap["type"].(string); ok {
					msgType = t
					h.mu.Lock()
					if msgType == "donation" || msgType == "text" {
						h.pendingDonation = message
						// Track when this display ends so new clients get remaining time only
						if dur, ok := msgMap["duration"].(float64); ok && dur > 0 {
							h.currentDisplayEndsAt = time.Now().Add(time.Duration(dur) * time.Millisecond)
						}
					} else if msgType == "media" {
						h.pendingMedia = message
					} else if msgType == "visibility" {
						if vis, _ := msgMap["visible"].(bool); !vis {
							h.pendingDonation = nil
							h.pendingMedia = nil
							h.currentDisplayEndsAt = time.Time{}
						}
					}
					h.mu.Unlock()
				}
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

// injectRemainingMs clones the donation message and adds remainingMs for newly connected clients
func injectRemainingMs(donationMsg []byte, remainingMs int) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(donationMsg, &m); err != nil {
		return nil
	}
	m[remainingMsKey] = remainingMs
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
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
func BroadcastDonation(id, donorName string, amount int, message string, durationMs int, paymentMethod, paymentType, plisioCurrency, plisioAmount string) {
	msg := DonationMessage{
		ID:             id,
		Type:           "donation",
		DonorName:      donorName,
		Amount:         amount,
		Message:        message,
		Duration:       durationMs,
		PaymentMethod:  paymentMethod,
		PaymentType:    paymentType,
		PlisioCurrency: plisioCurrency,
		PlisioAmount:   plisioAmount,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling donation message: %v", err)
		return
	}

	hub.broadcast <- data
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

	// Include payment info if available
	if history.Payment != nil {
		message.PaymentMethod = history.Payment.PaymentMethod
		message.PaymentType = history.Payment.PaymentType
		message.PaymentID = history.Payment.ID

		// Include crypto info if payment is crypto
		if history.Payment.PaymentMethod == "crypto" && history.Payment.PaymentType == "plisio" {
			// Use stored Plisio currency and amount from database
			if history.Payment.PlisioCurrency != "" {
				message.PlisioCurrency = history.Payment.PlisioCurrency
			}
			if history.Payment.PlisioSourceAmount > 0 {
				// Format amount with appropriate decimal places
				message.PlisioAmount = fmt.Sprintf("%.8f", history.Payment.PlisioSourceAmount)
				// Remove trailing zeros
				message.PlisioAmount = strings.TrimRight(strings.TrimRight(message.PlisioAmount, "0"), ".")
			}
		}
	}

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling history message: %v", err)
		return
	}

	hub.broadcast <- data
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
}

// BroadcastText sends a text-only donation message to all connected clients
func BroadcastText(id, donorName string, amount int, message string, durationMs int, paymentMethod, paymentType, plisioCurrency, plisioAmount string) {
	msg := DonationMessage{
		ID:             id,
		Type:           "text",
		DonorName:      donorName,
		Amount:         amount,
		Message:        message,
		Duration:       durationMs,
		PaymentMethod:  paymentMethod,
		PaymentType:    paymentType,
		PlisioCurrency: plisioCurrency,
		PlisioAmount:   plisioAmount,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling text message: %v", err)
		return
	}

	hub.broadcast <- data
}

// BroadcastClearQueue sends a clear queue message to all connected clients
func BroadcastClearQueue() {
	msg := DonationMessage{
		Type: "clear_queue",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling clear queue message: %v", err)
		return
	}

	// Clear pending messages
	hub.mu.Lock()
	hub.pendingDonation = nil
	hub.pendingMedia = nil
	hub.mu.Unlock()

	hub.broadcast <- data
	log.Println("📢 Clear queue message broadcasted to all WebSocket clients")
}
