package main

import (
	"encoding/json"
	"log"
	"net/http"
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
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// Client is a middleman between the websocket connection and the hub
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// DonationMessage represents a donation notification
type DonationMessage struct {
	Type       string `json:"type"` // "donation", "media", "visibility", "time", "gif", "text"
	DonorName  string `json:"donorName,omitempty"`
	Amount     int    `json:"amount,omitempty"` // Integer amount
	Message    string `json:"message,omitempty"`
	MediaURL   string `json:"mediaUrl,omitempty"`
	MediaType  string `json:"mediaType,omitempty"` // "image", "video", "youtube", "instagram", or "tiktok"
	Visible    bool   `json:"visible,omitempty"`
	TargetTime string `json:"targetTime,omitempty"` // For time countdown: "YYYY-MM-DDTHH:mm:ss"
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
			h.mu.Unlock()
			log.Printf("Client connected. Total clients: %d", count)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("Client disconnected. Total clients: %d", count)

		case message := <-h.broadcast:
			h.mu.RLock()
			clients := make([]*Client, 0, len(h.clients))
			for client := range h.clients {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			for _, client := range clients {
				select {
				case client.send <- message:
				default:
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
	defer c.conn.Close()

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
func BroadcastDonation(donorName string, amount int, message string) {
	msg := DonationMessage{
		Type:      "donation",
		DonorName: donorName,
		Amount:    amount,
		Message:   message,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling donation message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted donation: %s - %d", donorName, amount)
}

// BroadcastMedia sends a media update to all connected clients (auto-visible)
func BroadcastMedia(mediaURL, mediaType string) {
	msg := DonationMessage{
		Type:      "media",
		MediaURL:  mediaURL,
		MediaType: mediaType,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling media message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted media: %s (%s)", mediaURL, mediaType)
}

// BroadcastVisibility sends a visibility update to all connected clients
func BroadcastVisibility(visible bool) {
	msg := DonationMessage{
		Type:    "visibility",
		Visible: visible,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling visibility message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted visibility: %v", visible)
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

// BroadcastText sends a text-only donation message to all connected clients
func BroadcastText(donorName string, amount int, message string) {
	msg := DonationMessage{
		Type:      "text",
		DonorName: donorName,
		Amount:    amount,
		Message:   message,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling text message: %v", err)
		return
	}

	hub.broadcast <- data
	log.Printf("Broadcasted text: %s - %d", donorName, amount)
}
