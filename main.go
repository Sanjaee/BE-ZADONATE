package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	// Start WebSocket hub
	go hub.run()

	// Set Gin mode
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

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

		// Broadcast media (will auto-show) - send seconds to frontend
		BroadcastMedia(mediaURL, mediaType, startTimeSeconds)

		// Broadcast donation message (will auto-show)
		BroadcastDonation(req.DonorName, req.Amount, req.Message)

		c.JSON(200, gin.H{
			"success":          true,
			"message":          "Donation notification broadcasted",
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

		// Validate message max 160 characters
		if len(req.Message) > 160 {
			c.JSON(400, gin.H{
				"success": false,
				"error":   "message must be maximum 160 characters",
			})
			return
		}

		// Broadcast text message (will auto-show with TTS)
		BroadcastText(req.DonorName, req.Amount, req.Message)

		c.JSON(200, gin.H{
			"success": true,
			"message": "Text donation notification broadcasted",
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("🚀 API running on :" + port)
	log.Println("🔌 WebSocket:")
	log.Println("   WS   /ws")
	log.Println("🎯 Hit Endpoints:")
	log.Println("   POST /hit/gif")
	log.Println("   POST /hit/time")

	r.Run(":" + port)
}
