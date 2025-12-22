package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	donationQueueName = "donations"
)

// calculateDisplayDuration calculates display duration based on donation amount
// 1000 = 10 seconds, setiap kelipatan 1000 = +10 seconds
func calculateDisplayDuration(amount int) time.Duration {
	if amount <= 0 {
		return 10 * time.Second // Default 10 seconds if invalid
	}

	// Calculate: amount / 1000 * 10 seconds
	// 1000 = 10 detik, 2000 = 20 detik, etc.
	durationSeconds := float64(amount) / 1000.0 * 10.0
	duration := time.Duration(durationSeconds) * time.Second

	// Minimum 10 seconds
	if duration < 10*time.Second {
		duration = 10 * time.Second
	}

	return duration
}

// DonationJob represents a donation job in the queue
type DonationJob struct {
	ID             string `json:"id"`   // UUID for tracking this donation
	Type           string `json:"type"` // "gif" or "text"
	MediaURL       string `json:"mediaUrl,omitempty"`
	MediaType      string `json:"mediaType,omitempty"`
	StartTime      int    `json:"startTime,omitempty"`
	DonorName      string `json:"donorName"`
	Amount         int    `json:"amount"`
	Message        string `json:"message,omitempty"`
	PaymentMethod  string `json:"paymentMethod,omitempty"`  // crypto, bank_transfer, gopay, etc
	PaymentType    string `json:"paymentType,omitempty"`    // plisio, midtrans
	PlisioCurrency string `json:"plisioCurrency,omitempty"` // BTC, ETH, SOL, etc
	PlisioAmount   string `json:"plisioAmount,omitempty"`   // Crypto amount (e.g., "0.001", "0.5")
}

var (
	rabbitmqConn *amqp.Connection
	rabbitmqChan *amqp.Channel
	// State management for pause/resume
	currentDonation *DonationJob
	isPaused        bool
	pauseMutex      sync.RWMutex
	pauseChan       chan bool // Channel to signal pause/resume
	// Queue control
	consumerTag     string // Store consumer tag for cancellation
	workerStopped   bool   // Flag to stop worker
	workerStopMutex sync.RWMutex
	workerStopChan  chan struct{} // Channel to signal worker stop
)

// InitRabbitMQ initializes RabbitMQ connection and queue
func InitRabbitMQ() error {
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		rabbitmqURL = "amqp://admin:admin@localhost:5672/"
	}

	var err error
	// Retry connection with exponential backoff
	for i := 0; i < 5; i++ {
		rabbitmqConn, err = amqp.Dial(rabbitmqURL)
		if err == nil {
			break
		}
		log.Printf("Failed to connect to RabbitMQ (attempt %d/5): %v", i+1, err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}

	if err != nil {
		return err
	}

	rabbitmqChan, err = rabbitmqConn.Channel()
	if err != nil {
		return err
	}

	// Declare queue (durable to survive broker restarts)
	_, err = rabbitmqChan.QueueDeclare(
		donationQueueName, // name
		true,              // durable
		false,             // delete when unused
		false,             // exclusive
		false,             // no-wait
		nil,               // arguments
	)

	if err != nil {
		return err
	}

	log.Println("✅ RabbitMQ connected and queue declared")

	// Initialize pause channel
	pauseChan = make(chan bool, 1)

	// Initialize worker stop channel
	workerStopChan = make(chan struct{})

	return nil
}

// PublishDonation publishes a donation job to the queue
func PublishDonation(job DonationJob) error {
	// Check if worker is stopped (queue is cleared)
	workerStopMutex.RLock()
	stopped := workerStopped
	workerStopMutex.RUnlock()

	if stopped {
		log.Printf("⚠️  Queue is cleared, donation rejected: %s - %d (ID: %s)", job.DonorName, job.Amount, job.ID)
		return fmt.Errorf("queue is cleared, donation rejected")
	}

	// Generate UUID if not provided
	if job.ID == "" {
		job.ID = uuid.New().String()
	}

	if rabbitmqChan == nil {
		// Fallback to direct broadcast if RabbitMQ not available
		log.Printf("⚠️  RabbitMQ not available, processing donation directly: %s - %d", job.DonorName, job.Amount)
		return processDonationDirectly(job)
	}

	body, err := json.Marshal(job)
	if err != nil {
		return err
	}

	err = rabbitmqChan.Publish(
		"",                // exchange
		donationQueueName, // routing key
		false,             // mandatory
		false,             // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent, // Make message persistent
			ContentType:  "application/json",
			Body:         body,
		},
	)

	if err != nil {
		log.Printf("Error publishing to RabbitMQ: %v, falling back to direct broadcast", err)
		// Fallback to direct broadcast if publish fails
		return processDonationDirectly(job)
	}

	return nil
}

// processDonationDirectly processes donation without queue (fallback when RabbitMQ unavailable)
// Note: History is already created in payment.go, so we only need to broadcast
func processDonationDirectly(job DonationJob) error {
	// Check if worker is stopped (queue is cleared)
	workerStopMutex.RLock()
	stopped := workerStopped
	workerStopMutex.RUnlock()

	if stopped {
		log.Printf("⚠️  Queue is cleared, donation rejected (direct): %s - %d (ID: %s)", job.DonorName, job.Amount, job.ID)
		return fmt.Errorf("queue is cleared, donation rejected")
	}

	// Calculate display duration based on donation amount
	waitDuration := calculateDisplayDuration(job.Amount)
	durationMs := int(waitDuration.Milliseconds())

	// History is already created in payment.go before calling PublishDonation
	// So we just need to broadcast the donation

	// Process the donation based on type
	if job.Type == "gif" {
		// Broadcast media first
		if job.MediaURL != "" {
			BroadcastMedia(job.ID, job.MediaURL, job.MediaType, job.StartTime)
			// Small delay to ensure media is shown first
			time.Sleep(500 * time.Millisecond)
		}
		// Then broadcast donation with duration
		BroadcastDonation(job.ID, job.DonorName, job.Amount, job.Message, durationMs, job.PaymentMethod, job.PaymentType, job.PlisioCurrency, job.PlisioAmount)
	} else if job.Type == "text" {
		// Broadcast text donation with duration
		BroadcastText(job.ID, job.DonorName, job.Amount, job.Message, durationMs, job.PaymentMethod, job.PaymentType, job.PlisioCurrency, job.PlisioAmount)
	} else {
		log.Printf("⚠️  Unknown donation type (direct): %s (ID: %s)", job.Type, job.ID)
		return nil
	}

	// Wait for donation duration to complete (server-side timing)
	// This ensures donations run even if no browser is open
	checkInterval := 100 * time.Millisecond
	startTime := time.Now()

	for {
		// Check if worker is stopped (queue cleared)
		workerStopMutex.RLock()
		stopped := workerStopped
		workerStopMutex.RUnlock()

		if stopped {
			log.Printf("⚠️  Queue cleared during donation wait, stopping donation: %s (ID: %s)", job.DonorName, job.ID)
			BroadcastVisibility(job.ID, false)
			return fmt.Errorf("queue cleared, donation stopped")
		}

		elapsed := time.Since(startTime)
		if elapsed >= waitDuration {
			// Duration completed - broadcast end message to stop donation on frontend
			log.Printf("⏰ Donation duration completed (direct) for %s (ID: %s), sending end signal", job.DonorName, job.ID)
			BroadcastVisibility(job.ID, false) // Hide donation when duration ends
			break
		}
		time.Sleep(checkInterval)
	}

	return nil
}

// StartDonationWorker starts a worker that consumes donations from the queue
// and processes them sequentially (one at a time)
func StartDonationWorker() {
	if rabbitmqChan == nil {
		log.Println("⚠️  RabbitMQ not available, worker not started")
		return
	}

	// Set QoS to process one message at a time
	err := rabbitmqChan.Qos(
		1,     // prefetch count
		0,     // prefetch size
		false, // global
	)
	if err != nil {
		log.Printf("Error setting QoS: %v", err)
		return
	}

	// Generate unique consumer tag
	consumerTag = fmt.Sprintf("donation-worker-%d", time.Now().UnixNano())

	msgs, err := rabbitmqChan.Consume(
		donationQueueName, // queue
		consumerTag,       // consumer tag (for cancellation)
		false,             // auto-ack (manual ack for reliability)
		false,             // exclusive
		false,             // no-local
		false,             // no-wait
		nil,               // args
	)

	if err != nil {
		log.Printf("Error registering consumer: %v", err)
		return
	}

	// Reset worker stopped flag
	workerStopMutex.Lock()
	workerStopped = false
	workerStopMutex.Unlock()

	log.Println("👷 Donation worker started, processing donations sequentially...")

	go func() {
	workerLoop:
		for {
			// Check if worker should stop
			workerStopMutex.RLock()
			stopped := workerStopped
			workerStopMutex.RUnlock()

			if stopped {
				log.Println("🛑 Worker stopped, no longer processing donations")
				return
			}

			select {
			case msg, ok := <-msgs:
				if !ok {
					log.Println("📭 Message channel closed")
					return
				}

				// Double-check if worker should stop before processing
				workerStopMutex.RLock()
				stopped = workerStopped
				workerStopMutex.RUnlock()

				if stopped {
					// Nack message and don't requeue
					msg.Nack(false, false)
					log.Println("🛑 Worker stopped, rejecting message")
					return
				}

				var job DonationJob
				if err := json.Unmarshal(msg.Body, &job); err != nil {
					log.Printf("❌ Error unmarshaling job: %v", err)
					msg.Nack(false, false) // Reject and don't requeue
					// Continue to next iteration (process next message)
					continue workerLoop
				}

				// Set current donation being processed
				pauseMutex.Lock()
				currentDonation = &job
				isPaused = false
				pauseMutex.Unlock()

				// Calculate display duration based on donation amount
				waitDuration := calculateDisplayDuration(job.Amount)
				durationMs := int(waitDuration.Milliseconds())

				// Save donation to history (only for gif and text)
				if job.Type == "gif" || job.Type == "text" {
					if err := SaveDonationHistory(job); err != nil {
						log.Printf("⚠️  Failed to save donation history: %v", err)
						// Continue processing even if history save fails
					} else {
						// Broadcast new history to WebSocket clients
						history, err := GetDonationHistoryByID(job.ID)
						if err == nil && history != nil {
							BroadcastHistory(history)
						}
					}
				}

				// Process the donation based on type
				if job.Type == "gif" {
					// Broadcast media first
					if job.MediaURL != "" {
						BroadcastMedia(job.ID, job.MediaURL, job.MediaType, job.StartTime)
						// Small delay to ensure media is shown first
						time.Sleep(500 * time.Millisecond)
					}
					// Then broadcast donation with duration
					BroadcastDonation(job.ID, job.DonorName, job.Amount, job.Message, durationMs, job.PaymentMethod, job.PaymentType, job.PlisioCurrency, job.PlisioAmount)
				} else if job.Type == "text" {
					// Broadcast text donation with duration
					BroadcastText(job.ID, job.DonorName, job.Amount, job.Message, durationMs, job.PaymentMethod, job.PaymentType, job.PlisioCurrency, job.PlisioAmount)
				} else {
					log.Printf("⚠️  Unknown donation type: %s (ID: %s)", job.Type, job.ID)
				}

				// Wait for donation to complete or be paused
				checkInterval := 100 * time.Millisecond
				startTime := time.Now()
				elapsedBeforePause := time.Duration(0)
				pauseStartTime := time.Time{}

				for {
					pauseMutex.RLock()
					paused := isPaused
					pauseMutex.RUnlock()

					if paused {
						// Paused - record elapsed time before pause
						if pauseStartTime.IsZero() {
							elapsedBeforePause = time.Since(startTime)
							pauseStartTime = time.Now()
						}
						// Wait for resume signal
						select {
						case <-pauseChan:
							// Resume signal received
							if !pauseStartTime.IsZero() {
								// Reset start time to continue from where we paused
								startTime = time.Now().Add(-elapsedBeforePause)
								pauseStartTime = time.Time{}
							}
						case <-time.After(checkInterval):
							// Timeout to check pause state again
							continue
						}
					} else {
						// Not paused - check if we've reached the duration
						elapsed := time.Since(startTime)
						if elapsed >= waitDuration {
							// Duration completed - broadcast end message to stop donation on frontend
							log.Printf("⏰ Donation duration completed for %s (ID: %s), sending end signal", job.DonorName, job.ID)
							BroadcastVisibility(job.ID, false) // Hide donation when duration ends
							break
						}
						time.Sleep(checkInterval)
					}
				}

				// Acknowledge message after successful processing
				msg.Ack(false)

				// Clear current donation
				pauseMutex.Lock()
				currentDonation = nil
				isPaused = false
				pauseMutex.Unlock()

				// Wait a bit before processing next donation (to ensure sequential display)
				time.Sleep(1 * time.Second)

			case <-workerStopChan:
				log.Println("🛑 Worker stop signal received")
				return
			}
		}
	}()
}

// PauseDonation pauses the currently processing donation
func PauseDonation() bool {
	pauseMutex.Lock()
	defer pauseMutex.Unlock()

	if currentDonation == nil {
		return false // No donation currently processing
	}

	if !isPaused {
		isPaused = true
		// Hide the donation with its UUID
		BroadcastVisibility(currentDonation.ID, false)
		log.Printf("⏸️  Paused donation: %s - %d (ID: %s)", currentDonation.DonorName, currentDonation.Amount, currentDonation.ID)
		return true
	}

	return false // Already paused
}

// ResumeDonation resumes the paused donation
func ResumeDonation() bool {
	pauseMutex.Lock()

	if currentDonation == nil {
		pauseMutex.Unlock()
		return false // No donation currently processing
	}

	if !isPaused {
		pauseMutex.Unlock()
		return false // Not paused
	}

	isPaused = false
	donationCopy := *currentDonation
	pauseMutex.Unlock()

	// Show the donation again with its UUID
	BroadcastVisibility(donationCopy.ID, true)

	// Signal resume to worker (non-blocking)
	select {
	case pauseChan <- true:
		log.Printf("▶️  Resumed donation: %s - %d", donationCopy.DonorName, donationCopy.Amount)
	default:
		// Channel full, but that's okay - worker will check isPaused state
		log.Printf("▶️  Resumed donation (signal sent): %s - %d", donationCopy.DonorName, donationCopy.Amount)
	}

	return true
}

// GetCurrentDonationStatus returns the current donation status
func GetCurrentDonationStatus() (bool, *DonationJob) {
	pauseMutex.RLock()
	defer pauseMutex.RUnlock()

	if currentDonation == nil {
		return false, nil
	}

	// Create a copy to avoid race conditions
	donationCopy := *currentDonation
	return isPaused, &donationCopy
}

// ClearQueue removes all messages from the donation queue and stops worker
func ClearQueue() error {
	// Stop worker first to prevent processing new messages
	workerStopMutex.Lock()
	workerStopped = true
	workerStopMutex.Unlock()

	// Signal worker to stop
	select {
	case workerStopChan <- struct{}{}:
	default:
	}

	// Cancel consumer to stop receiving new messages
	if rabbitmqChan != nil && consumerTag != "" {
		if err := rabbitmqChan.Cancel(consumerTag, false); err != nil {
			log.Printf("⚠️  Error canceling consumer: %v", err)
		} else {
			log.Printf("🛑 Consumer '%s' canceled", consumerTag)
		}
	}

	// Purge all messages from the queue (if RabbitMQ is available)
	if rabbitmqChan != nil {
		_, err := rabbitmqChan.QueuePurge(donationQueueName, false)
		if err != nil {
			log.Printf("Error purging queue: %v", err)
			// Continue anyway to broadcast clear message
		} else {
			log.Println("🗑️  All messages cleared from donation queue")
		}
	} else {
		log.Println("🗑️  Queue cleared (RabbitMQ not available)")
	}

	// Reset current donation state
	ResetCurrentDonation()

	// Broadcast clear queue message to all WebSocket clients (ALWAYS, even if RabbitMQ not available)
	BroadcastClearQueue()

	// Restart worker after a short delay (if RabbitMQ is available)
	if rabbitmqChan != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			StartDonationWorker()
		}()
	} else {
		// Reset worker stopped flag so new donations can be processed directly
		go func() {
			time.Sleep(100 * time.Millisecond)
			workerStopMutex.Lock()
			workerStopped = false
			workerStopMutex.Unlock()
			log.Println("✅ Worker state reset (RabbitMQ not available, direct processing enabled)")
		}()
	}

	return nil
}

// ResetCurrentDonation resets the current donation state
func ResetCurrentDonation() {
	pauseMutex.Lock()
	defer pauseMutex.Unlock()

	if currentDonation != nil {
		log.Printf("🔄 Resetting current donation: %s - %d (ID: %s)",
			currentDonation.DonorName, currentDonation.Amount, currentDonation.ID)
	}

	currentDonation = nil
	isPaused = false

	// Clear pause channel
	select {
	case <-pauseChan:
	default:
	}

	log.Println("✅ Current donation state reset")
}

// ClearAllQueuesAndReset clears all queues and resets state
func ClearAllQueuesAndReset() error {
	// Clear RabbitMQ queue
	if err := ClearQueue(); err != nil {
		return err
	}

	// Reset current donation state
	ResetCurrentDonation()

	return nil
}

// CloseRabbitMQ closes RabbitMQ connection
func CloseRabbitMQ() {
	if rabbitmqChan != nil {
		rabbitmqChan.Close()
	}
	if rabbitmqConn != nil {
		rabbitmqConn.Close()
	}
}
