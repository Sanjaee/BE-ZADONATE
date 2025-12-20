package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// detectMediaType detects if URL is video, image, YouTube, Instagram Reels, or TikTok based on URL
func detectMediaType(url string) string {
	urlLower := strings.ToLower(url)

	// Check for YouTube
	if strings.Contains(urlLower, "youtube.com") || strings.Contains(urlLower, "youtu.be") {
		return "youtube"
	}

	// Check for Instagram Reels
	if strings.Contains(urlLower, "instagram.com/reel/") || strings.Contains(urlLower, "instagram.com/p/") {
		return "instagram"
	}

	// Check for TikTok
	if strings.Contains(urlLower, "tiktok.com") {
		return "tiktok"
	}

	// Video extensions
	videoExts := []string{".mp4", ".webm", ".ogg", ".mov", ".avi", ".mkv", ".flv", ".wmv", ".m4v", ".3gp"}
	for _, ext := range videoExts {
		if strings.HasSuffix(urlLower, ext) || strings.Contains(urlLower, ext+"?") {
			return "video"
		}
	}

	// Image extensions
	imageExts := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp", ".ico", ".tiff", ".tif"}
	for _, ext := range imageExts {
		if strings.HasSuffix(urlLower, ext) || strings.Contains(urlLower, ext+"?") {
			return "image"
		}
	}

	// Default to image if cannot detect
	return "image"
}

func main() {
	// Initialize Database
	if err := InitDatabase(); err != nil {
		log.Printf("⚠️  Warning: Database initialization failed: %v. History will not be saved.", err)
	}

	// Initialize RabbitMQ
	if err := InitRabbitMQ(); err != nil {
		log.Printf("⚠️  Warning: RabbitMQ initialization failed: %v. Donations will be processed directly.", err)
	} else {
		// Start donation worker to process queue sequentially
		StartDonationWorker()
	}

	// Start WebSocket hub
	go hub.run()

	// Set Gin mode
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.String(200, "OK")
	})

	// ========== WEBSOCKET ENDPOINTS ==========

	// WebSocket endpoint for realtime updates
	r.GET("/ws", ServeWS)

	// ========== HIT ENDPOINTS ==========

	// HIT GIF - Trigger donation with image/video (realtime)
	r.POST("/hit/gif", func(c *gin.Context) {
		var req struct {
			ImageURL   string      `json:"imageUrl,omitempty"` // Image/Video URL (legacy support)
			MediaURL   string      `json:"mediaUrl,omitempty"` // Image/Video URL
			DonorName  string      `json:"donorName"`          // Donor name
			Amount     int         `json:"amount"`             // Donation amount (integer)
			Message    string      `json:"message,omitempty"`
			StartTime  int         `json:"startTime,omitempty"`  // Start time in MINUTES for YouTube videos (legacy)
			TargetTime interface{} `json:"targetTime,omitempty"` // Start time in MINUTES for YouTube videos (can be string or int)
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request body",
			})
			return
		}

		// Support both imageUrl (legacy) and mediaUrl
		mediaURL := req.MediaURL
		if mediaURL == "" {
			mediaURL = req.ImageURL
		}

		if mediaURL == "" || req.DonorName == "" || req.Amount <= 0 {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "mediaUrl (or imageUrl), donorName, and amount (positive integer) are required",
			})
			return
		}

		// Validate message max 160 characters
		if len(req.Message) > 160 {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "message must be maximum 160 characters",
			})
			return
		}

		// Auto-detect media type from URL
		mediaType := detectMediaType(mediaURL)

		// Parse targetTime (can be string or int) - prioritize targetTime over startTime
		// Note: startTime is in MINUTES, will be converted to seconds for YouTube
		var startTimeMinutes int
		if req.TargetTime != nil {
			switch v := req.TargetTime.(type) {
			case string:
				// Try to parse string as int (minutes)
				if parsed, err := strconv.Atoi(v); err == nil {
					startTimeMinutes = parsed
				}
			case float64:
				// JSON numbers come as float64 (minutes)
				startTimeMinutes = int(v)
			case int:
				startTimeMinutes = v
			}
		} else if req.StartTime > 0 {
			// Fallback to legacy startTime (in minutes)
			startTimeMinutes = req.StartTime
		}

		// Validate startTimeMinutes (must be >= 0)
		if startTimeMinutes < 0 {
			startTimeMinutes = 0
		}

		// Convert minutes to seconds for YouTube API
		startTimeSeconds := startTimeMinutes * 60

		// Publish to RabbitMQ queue instead of direct broadcast
		job := DonationJob{
			Type:      "gif",
			MediaURL:  mediaURL,
			MediaType: mediaType,
			StartTime: startTimeSeconds,
			DonorName: req.DonorName,
			Amount:    req.Amount,
			Message:   req.Message,
		}

		if err := PublishDonation(job); err != nil {
			// Fallback to direct broadcast if RabbitMQ fails
			log.Printf("⚠️  RabbitMQ publish failed, using direct broadcast: %v", err)
			donationID := uuid.New().String()
			// Calculate duration for fallback
			durationMs := int((float64(req.Amount) / 1000.0 * 10.0) * 1000)
			if durationMs < 10000 {
				durationMs = 10000
			}
			BroadcastMedia(donationID, mediaURL, mediaType, startTimeSeconds)
			time.Sleep(500 * time.Millisecond)
			BroadcastDonation(donationID, req.DonorName, req.Amount, req.Message, durationMs)
		}

		c.JSON(200, gin.H{
			"success":          true,
			"message":          "Donation queued for processing",
			"mediaType":        mediaType,
			"startTimeMinutes": startTimeMinutes,
			"startTimeSeconds": startTimeSeconds,
		})
	})

	// HIT TIME - Set countdown timer target (realtime)
	r.POST("/hit/time", func(c *gin.Context) {
		var req struct {
			TargetTime string `json:"targetTime"` // Format: "YYYY-MM-DDTHH:mm:ss" or "YYYY-MM-DD HH:mm:ss"
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request body",
			})
			return
		}

		if req.TargetTime == "" {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "targetTime is required",
			})
			return
		}

		// Validate and normalize time format
		_, err := time.Parse("2006-01-02T15:04:05", req.TargetTime)
		if err != nil {
			// Try alternative format
			_, err = time.Parse("2006-01-02 15:04:05", req.TargetTime)
			if err != nil {
				c.JSON(400, gin.H{
					"success": false,
					"error":   "Invalid time format. Use YYYY-MM-DDTHH:mm:ss or YYYY-MM-DD HH:mm:ss",
				})
				return
			}
			// Normalize to ISO format
			t, _ := time.Parse("2006-01-02 15:04:05", req.TargetTime)
			req.TargetTime = t.Format("2006-01-02T15:04:05")
		}

		// Broadcast time target to all WebSocket clients
		BroadcastTime(req.TargetTime)

		c.JSON(200, gin.H{
			"success":    true,
			"message":    "Time countdown target broadcasted",
			"targetTime": req.TargetTime,
		})
	})

	// HIT PAUSE - Pause the currently processing donation
	r.POST("/hit/pause", func(c *gin.Context) {
		success := PauseDonation()
		if !success {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "No donation currently processing or already paused",
			})
			return
		}

		isPaused, donation := GetCurrentDonationStatus()
		c.JSON(200, gin.H{
			"success":  true,
			"message":  "Donation paused",
			"isPaused": isPaused,
			"donation": donation,
		})
	})

	// HIT RESUME - Resume the paused donation
	r.POST("/hit/resume", func(c *gin.Context) {
		success := ResumeDonation()
		if !success {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "No donation currently processing or not paused",
			})
			return
		}

		isPaused, donation := GetCurrentDonationStatus()
		c.JSON(200, gin.H{
			"success":  true,
			"message":  "Donation resumed",
			"isPaused": isPaused,
			"donation": donation,
		})
	})

	// HIT STATUS - Get current donation status
	r.GET("/hit/status", func(c *gin.Context) {
		isPaused, donation := GetCurrentDonationStatus()
		if donation == nil {
			c.JSON(200, gin.H{
				"success":  true,
				"message":  "No donation currently processing",
				"isPaused": false,
				"donation": nil,
			})
			return
		}

		c.JSON(200, gin.H{
			"success":  true,
			"isPaused": isPaused,
			"donation": donation,
		})
	})

	// HIT RESET - Clear all queues and reset state
	r.POST("/hit/reset", func(c *gin.Context) {
		if err := ClearAllQueuesAndReset(); err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to clear queues and reset: " + err.Error(),
			})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": "All queues cleared and state reset",
		})
	})

	// HIT TEXT - Trigger text-only donation alert with TTS (realtime)
	r.POST("/hit/text", func(c *gin.Context) {
		var req struct {
			DonorName string `json:"donorName"` // Donor name
			Amount    int    `json:"amount"`    // Donation amount (integer)
			Message   string `json:"message,omitempty"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request body",
			})
			return
		}

		if req.DonorName == "" || req.Amount <= 0 {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "donorName and amount (positive integer) are required",
			})
			return
		}

		// Validate message max 250 characters (only for text donations)
		if len(req.Message) > 250 {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "message must be maximum 250 characters",
			})
			return
		}

		// Publish to RabbitMQ queue instead of direct broadcast
		job := DonationJob{
			Type:      "text",
			DonorName: req.DonorName,
			Amount:    req.Amount,
			Message:   req.Message,
		}

		if err := PublishDonation(job); err != nil {
			// Fallback to direct broadcast if RabbitMQ fails
			log.Printf("⚠️  RabbitMQ publish failed, using direct broadcast: %v", err)
			donationID := uuid.New().String()
			// Calculate duration for fallback
			durationMs := int((float64(req.Amount) / 1000.0 * 10.0) * 1000)
			if durationMs < 10000 {
				durationMs = 10000
			}
			BroadcastText(donationID, req.DonorName, req.Amount, req.Message, durationMs)
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": "Text donation queued for processing",
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}

	// HIT HISTORY - Get donation history (gif and text only)
	r.GET("/hit/history", func(c *gin.Context) {
		limitStr := c.DefaultQuery("limit", "50")
		offsetStr := c.DefaultQuery("offset", "0")

		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit <= 0 || limit > 100 {
			limit = 50
		}

		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			offset = 0
		}

		history, err := GetDonationHistory(limit, offset)
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to retrieve donation history: " + err.Error(),
			})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"data":    history,
			"limit":   limit,
			"offset":  offset,
		})
	})

	// PAYMENT ENDPOINTS
	// Create payment
	r.POST("/payment/create", func(c *gin.Context) {
		var req CreatePaymentRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request: " + err.Error(),
			})
			return
		}

		payment, err := CreatePayment(req)
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to create payment: " + err.Error(),
			})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"data":    payment,
		})
	})

	// Debug endpoint to list all payments (remove in production)
	r.GET("/payment/debug/list", func(c *gin.Context) {
		var payments []Payment
		err := db.Find(&payments).Error
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		// Also get raw data to see actual database values
		var rawPayments []map[string]interface{}
		db.Raw("SELECT id, order_id, donor_name, amount, status FROM payments ORDER BY created_at DESC LIMIT 10").Scan(&rawPayments)

		c.JSON(200, gin.H{
			"success": true,
			"count":   len(payments),
			"data":    payments,
			"raw":     rawPayments,
		})
	})

	// Get payment by ID (UUID) or Order ID
	r.GET("/payment/:id", func(c *gin.Context) {
		idStr := c.Param("id")

		var payment *Payment
		var err error

		// Check if it's an Order ID (starts with "DONATE_")
		if strings.HasPrefix(idStr, "DONATE_") {
			payment, err = GetPaymentByOrderID(idStr)
		} else {
			// Try as Payment ID (UUID)
			payment, err = GetPaymentByID(idStr)
			// If not found as UUID, try as Order ID (for backward compatibility)
			if err != nil {
				payment, err = GetPaymentByOrderID(idStr)
			}
		}

		if err != nil {
			c.JSON(404, gin.H{
				"success": false,
				"error":   "Payment not found",
				"id":      idStr,
			})
			return
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    payment,
		})
	})

	// Check payment status from Midtrans API (for manual polling/testing)
	r.POST("/payment/check-status", func(c *gin.Context) {
		var req struct {
			OrderID string `json:"orderId" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request: " + err.Error(),
			})
			return
		}

		if err := CheckPaymentStatusFromMidtrans(req.OrderID); err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to check payment status: " + err.Error(),
			})
			return
		}

		// Get updated payment
		payment, err := GetPaymentByOrderID(req.OrderID)
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to get payment: " + err.Error(),
			})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": "Payment status checked and updated",
			"data":    payment,
		})
	})

	// Midtrans webhook
	r.POST("/payment/webhook", func(c *gin.Context) {
		var webhookData map[string]interface{}
		if err := c.ShouldBindJSON(&webhookData); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid webhook data",
			})
			return
		}

		webhookJSON, _ := json.Marshal(webhookData)

		orderID, ok := webhookData["order_id"].(string)
		if !ok {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Missing order_id",
			})
			return
		}

		transactionStatus, _ := webhookData["transaction_status"].(string)
		transactionID, _ := webhookData["transaction_id"].(string)

		var vaNumber, bankType, qrCodeURL string
		if vaNumbers, ok := webhookData["va_numbers"].([]interface{}); ok && len(vaNumbers) > 0 {
			if va, ok := vaNumbers[0].(map[string]interface{}); ok {
				vaNumber, _ = va["va_number"].(string)
				bankType, _ = va["bank"].(string)
			}
		}

		if actions, ok := webhookData["actions"].([]interface{}); ok {
			for _, action := range actions {
				if act, ok := action.(map[string]interface{}); ok {
					if name, _ := act["name"].(string); name == "generate-qr-code" {
						qrCodeURL, _ = act["url"].(string)
						break
					}
				}
			}
		}

		var expiryTime *time.Time
		if expiry, ok := webhookData["expiry_time"].(string); ok && expiry != "" {
			exp, err := time.Parse(time.RFC3339, expiry)
			if err == nil {
				expiryTime = &exp
			}
		}

		if err := UpdatePaymentStatus(orderID, transactionStatus, transactionID, vaNumber, bankType, qrCodeURL, expiryTime, string(webhookJSON)); err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to update payment: " + err.Error(),
			})
			return
		}
		c.JSON(200, gin.H{
			"success": true,
			"message": "Webhook processed",
		})
	})

	// ========== PLISIO CRYPTO PAYMENT ENDPOINTS ==========

	// Create Plisio invoice
	r.POST("/payment/plisio/create", func(c *gin.Context) {
		var req PlisioCreateInvoiceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid request: " + err.Error(),
			})
			return
		}

		payment, invoiceData, err := CreatePlisioInvoice(req)
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to create Plisio invoice: " + err.Error(),
			})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"payment":    payment,
				"invoice":    invoiceData,
				"invoiceUrl": invoiceData.InvoiceURL,
				"txnId":      invoiceData.TxnID,
			},
		})
	})

	// Plisio webhook
	r.POST("/payment/plisio/webhook", func(c *gin.Context) {
		var webhookData map[string]interface{}
		if err := c.ShouldBindJSON(&webhookData); err != nil {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Invalid webhook data",
			})
			return
		}

		// Verify callback data
		if !VerifyPlisioCallback(webhookData) {
			log.Printf("❌ Invalid Plisio callback verification")
			c.JSON(422, gin.H{
				"success": false,
				"error":   "Invalid callback verification",
			})
			return
		}

		// Parse callback data
		var callbackData PlisioCallbackData
		callbackJSON, _ := json.Marshal(webhookData)
		if err := json.Unmarshal(callbackJSON, &callbackData); err != nil {
			log.Printf("❌ Failed to parse callback data: %v", err)
			c.JSON(400, gin.H{
				"success": false,
				"error":   "Failed to parse callback data",
			})
			return
		}

		if err := UpdatePaymentStatusFromPlisio(callbackData); err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to update payment: " + err.Error(),
			})
			return
		}
		c.JSON(200, gin.H{
			"success": true,
			"message": "Webhook processed",
		})
	})

	// Get supported cryptocurrencies
	r.GET("/payment/plisio/currencies", func(c *gin.Context) {
		sourceCurrency := c.DefaultQuery("sourceCurrency", "")

		currencies, err := GetPlisioCurrencies(sourceCurrency)
		if err != nil {
			c.JSON(500, gin.H{
				"success": false,
				"error":   "Failed to fetch currencies: " + err.Error(),
			})
			return
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    currencies,
			"count":   len(currencies),
		})
	})

	log.Println("🚀 API running on :" + port)
	log.Println("🔌 WebSocket:")
	log.Println("   WS   /ws")
	log.Println("🎯 Hit Endpoints:")
	log.Println("   POST /hit/gif")
	log.Println("   POST /hit/time")
	log.Println("   POST /hit/text")
	log.Println("   POST /hit/pause")
	log.Println("   POST /hit/resume")
	log.Println("   GET  /hit/status")
	log.Println("   POST /hit/reset")
	log.Println("   GET  /hit/history")
	log.Println("💳 Payment Endpoints:")
	log.Println("   POST /payment/create")
	log.Println("   GET  /payment/:id")
	log.Println("   POST /payment/webhook")
	log.Println("   POST /payment/check-status (manual status check)")
	log.Println("🪙 Plisio Crypto Payment Endpoints:")
	log.Println("   POST /payment/plisio/create")
	log.Println("   POST /payment/plisio/webhook")
	log.Println("   GET  /payment/plisio/currencies")

	r.Run(":" + port)
}
