package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	MIDTRANS_SERVER_KEY = "SB-Mid-server-4zIt7djwCeRdMpgF4gXDjciC"
	MIDTRANS_BASE_URL   = "https://api.sandbox.midtrans.com/v2"
)

// MidtransChargeRequest represents Midtrans charge request
type MidtransChargeRequest struct {
	PaymentType        string                     `json:"payment_type"`
	TransactionDetails MidtransTransactionDetails `json:"transaction_details"`
	CustomerDetails    MidtransCustomerDetails    `json:"customer_details"`
	ItemDetails        []MidtransItemDetail       `json:"item_details"`
	BankTransfer       *MidtransBankTransfer      `json:"bank_transfer,omitempty"`
	Gopay              *MidtransGopay             `json:"gopay,omitempty"`
	CreditCard         *MidtransCreditCard        `json:"credit_card,omitempty"`
}

type MidtransTransactionDetails struct {
	OrderID     string `json:"order_id"`
	GrossAmount int    `json:"gross_amount"`
}

type MidtransCustomerDetails struct {
	FirstName string `json:"first_name"`
	Email     string `json:"email"`
}

type MidtransItemDetail struct {
	ID       string `json:"id"`
	Price    int    `json:"price"`
	Quantity int    `json:"quantity"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type MidtransBankTransfer struct {
	Bank string `json:"bank"`
}

type MidtransGopay struct {
	EnableCallback bool   `json:"enable_callback"`
	CallbackURL    string `json:"callback_url"`
}

type MidtransCreditCard struct {
	Secure         bool `json:"secure"`
	Authentication bool `json:"authentication"`
}

// MidtransChargeResponse represents Midtrans charge response
type MidtransChargeResponse struct {
	TransactionID     string             `json:"transaction_id"`
	OrderID           string             `json:"order_id"`
	GrossAmount       string             `json:"gross_amount"`
	PaymentType       string             `json:"payment_type"`
	TransactionTime   string             `json:"transaction_time"`
	TransactionStatus string             `json:"transaction_status"`
	FraudStatus       string             `json:"fraud_status"`
	StatusMessage     string             `json:"status_message"`
	VANumbers         []MidtransVANumber `json:"va_numbers,omitempty"`
	Actions           []MidtransAction   `json:"actions,omitempty"`
	ExpiryTime        string             `json:"expiry_time,omitempty"`
}

type MidtransVANumber struct {
	Bank     string `json:"bank"`
	VANumber string `json:"va_number"`
}

type MidtransAction struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	URL    string `json:"url"`
}

// CreatePaymentRequest represents payment creation request
type CreatePaymentRequest struct {
	DonorName     string `json:"donorName" binding:"required"`
	DonorEmail    string `json:"donorEmail"`
	Amount        int    `json:"amount" binding:"required,min=1000"`
	DonationType  string `json:"donationType" binding:"required,oneof=gif text"`
	MediaURL      string `json:"mediaUrl,omitempty"`
	MediaType     string `json:"mediaType,omitempty"`
	StartTime     int    `json:"startTime,omitempty"`
	Message       string `json:"message,omitempty"`
	Notes         string `json:"notes,omitempty"`
	PaymentMethod string `json:"paymentMethod" binding:"required,oneof=bank_transfer gopay credit_card qris"`
	Bank          string `json:"bank,omitempty"` // bca, bni, mandiri, etc
}

// mapMidtransStatusToPaymentStatus maps Midtrans status to PaymentStatus
func mapMidtransStatusToPaymentStatus(status string) PaymentStatus {
	switch status {
	case "pending":
		return PaymentStatusPending
	case "settlement", "capture":
		return PaymentStatusSuccess
	case "deny":
		return PaymentStatusFailed
	case "cancel":
		return PaymentStatusCancelled
	case "expire":
		return PaymentStatusExpired
	default:
		return PaymentStatusPending
	}
}

// CreatePayment creates a payment and charges to Midtrans
func CreatePayment(req CreatePaymentRequest) (*Payment, error) {
	// Generate order ID with UUID
	orderID := fmt.Sprintf("DONATE_%s", uuid.New().String())

	// Prepare charge data
	chargeData := MidtransChargeRequest{
		PaymentType: req.PaymentMethod,
		TransactionDetails: MidtransTransactionDetails{
			OrderID:     orderID,
			GrossAmount: req.Amount,
		},
		CustomerDetails: MidtransCustomerDetails{
			FirstName: req.DonorName,
			Email:     req.DonorEmail,
		},
		ItemDetails: []MidtransItemDetail{
			{
				ID:       "donation",
				Price:    req.Amount,
				Quantity: 1,
				Name:     fmt.Sprintf("Donation %s", req.DonationType),
				Category: "donation",
			},
		},
	}

	// Add payment method specific config
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	switch req.PaymentMethod {
	case "bank_transfer":
		bank := req.Bank
		if bank == "" {
			bank = "bca"
		}
		chargeData.BankTransfer = &MidtransBankTransfer{Bank: bank}
	case "gopay":
		chargeData.Gopay = &MidtransGopay{
			EnableCallback: true,
			CallbackURL:    fmt.Sprintf("%s/payment/callback", frontendURL),
		}
	case "qris":
		// QRIS uses qris payment type in Midtrans (same as gopay but with qris type)
		chargeData.PaymentType = "qris"
		// QRIS can use gopay configuration for callback
		chargeData.Gopay = &MidtransGopay{
			EnableCallback: true,
			CallbackURL:    fmt.Sprintf("%s/payment/callback", frontendURL),
		}
	case "credit_card":
		chargeData.CreditCard = &MidtransCreditCard{
			Secure:         true,
			Authentication: true,
		}
	}

	// Create payment record first
	paymentID := uuid.New().String()
	payment := Payment{
		ID:            paymentID,
		OrderID:       orderID,
		DonorName:     req.DonorName,
		DonorEmail:    req.DonorEmail,
		Amount:        req.Amount,
		TotalAmount:   req.Amount,
		Status:        PaymentStatusPending,
		PaymentMethod: req.PaymentMethod,
		PaymentType:   "midtrans",
		DonationType:  req.DonationType,
		MediaURL:      req.MediaURL,
		MediaType:     req.MediaType,
		StartTime:     req.StartTime,
		Message:       req.Message,
		Notes:         req.Notes,
	}

	if err := db.Create(&payment).Error; err != nil {
		log.Printf("❌ Failed to create payment: %v", err)
		return nil, fmt.Errorf("failed to create payment: %v", err)
	}

	// Charge to Midtrans
	chargeJSON, err := json.Marshal(chargeData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal charge data: %v", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(MIDTRANS_SERVER_KEY + ":"))

	// Make HTTP request to Midtrans
	reqHTTP, err := http.NewRequest("POST", MIDTRANS_BASE_URL+"/charge", bytes.NewBuffer(chargeJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	reqHTTP.Header.Set("Authorization", "Basic "+auth)
	reqHTTP.Header.Set("Content-Type", "application/json")
	reqHTTP.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(reqHTTP)
	if err != nil {
		log.Printf("⚠️  Failed to charge Midtrans: %v", err)
		return &payment, nil // Return payment even if Midtrans fails
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("⚠️  Failed to read Midtrans response: %v", err)
		return &payment, nil
	}

	var midtransResp MidtransChargeResponse
	if err := json.Unmarshal(body, &midtransResp); err != nil {
		log.Printf("⚠️  Failed to parse Midtrans response: %v", err)
		return &payment, nil
	}

	// Update payment with Midtrans response
	var vaNumber, bankType, qrCodeURL string
	if len(midtransResp.VANumbers) > 0 {
		vaNumber = midtransResp.VANumbers[0].VANumber
		bankType = midtransResp.VANumbers[0].Bank
	}

	// Extract QR code URL from actions (for Gopay)
	// Try multiple action names as fallback
	for _, action := range midtransResp.Actions {
		if action.Name == "generate-qr-code" || action.Name == "generate-qr-code-v2" {
			qrCodeURL = action.URL
			break
		}
	}
	// If not found by name, try by method GET
	if qrCodeURL == "" {
		for _, action := range midtransResp.Actions {
			if action.Method == "GET" && action.URL != "" {
				qrCodeURL = action.URL
				break
			}
		}
	}

	var expiryTime *time.Time
	if midtransResp.ExpiryTime != "" {
		// Try multiple time formats
		formats := []string{
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
		}
		for _, format := range formats {
			exp, err := time.Parse(format, midtransResp.ExpiryTime)
			if err == nil {
				expiryTime = &exp
				break
			}
		}
	}

	updateData := map[string]interface{}{
		"midtrans_transaction_id": midtransResp.TransactionID,
		"status":                  mapMidtransStatusToPaymentStatus(midtransResp.TransactionStatus),
		"fraud_status":            midtransResp.FraudStatus,
		"midtrans_response":       string(body),
		"va_number":               vaNumber,
		"bank_type":               bankType,
		"qr_code_url":             qrCodeURL,
		"expiry_time":             expiryTime,
		"updated_at":              time.Now(),
	}

	if err := db.Model(&payment).Updates(updateData).Error; err != nil {
		log.Printf("⚠️  Failed to update payment: %v", err)
	}

	return &payment, nil
}

// GetPaymentByID retrieves payment by ID
func GetPaymentByID(id string) (*Payment, error) {
	var payment Payment
	err := db.Where("id = ?", id).First(&payment).Error
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

// GetPaymentByOrderID retrieves payment by order ID
func GetPaymentByOrderID(orderID string) (*Payment, error) {
	var payment Payment
	err := db.Where("order_id = ?", orderID).First(&payment).Error
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

// CheckPaymentStatusFromMidtrans checks payment status from Midtrans API
func CheckPaymentStatusFromMidtrans(orderID string) error {
	// Get payment from database first
	var payment Payment
	if err := db.Where("order_id = ?", orderID).First(&payment).Error; err != nil {
		return fmt.Errorf("payment not found: %v", err)
	}

	// If already successful, skip check
	if payment.Status == PaymentStatusSuccess {
		return nil
	}

	// Call Midtrans status API
	auth := base64.StdEncoding.EncodeToString([]byte(MIDTRANS_SERVER_KEY + ":"))
	url := fmt.Sprintf("%s/%s/status", MIDTRANS_BASE_URL, payment.MidtransTransactionID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call Midtrans API: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %v", err)
	}

	if resp.StatusCode != 200 {
		log.Printf("⚠️  Midtrans API returned status %d: %s", resp.StatusCode, string(body))
		return fmt.Errorf("Midtrans API error: %s", string(body))
	}

	var midtransResp map[string]interface{}
	if err := json.Unmarshal(body, &midtransResp); err != nil {
		return fmt.Errorf("failed to parse response: %v", err)
	}

	// Extract status information
	transactionStatus, _ := midtransResp["transaction_status"].(string)
	transactionID, _ := midtransResp["transaction_id"].(string)

	var vaNumber, bankType, qrCodeURL string
	if vaNumbers, ok := midtransResp["va_numbers"].([]interface{}); ok && len(vaNumbers) > 0 {
		if va, ok := vaNumbers[0].(map[string]interface{}); ok {
			vaNumber, _ = va["va_number"].(string)
			bankType, _ = va["bank"].(string)
		}
	}

	// Extract QR code URL from actions (preserve existing if not found in new response)
	if actions, ok := midtransResp["actions"].([]interface{}); ok && len(actions) > 0 {
		for _, action := range actions {
			if act, ok := action.(map[string]interface{}); ok {
				name, _ := act["name"].(string)
				url, _ := act["url"].(string)
				// Try multiple action names
				if (name == "generate-qr-code" || name == "generate-qr-code-v2") && url != "" {
					qrCodeURL = url
					log.Printf("✅ Found QR code URL from action '%s': %s", name, qrCodeURL)
					break
				}
			}
		}
		// If not found by name, try by method GET
		if qrCodeURL == "" {
			for _, action := range actions {
				if act, ok := action.(map[string]interface{}); ok {
					method, _ := act["method"].(string)
					url, _ := act["url"].(string)
					if method == "GET" && url != "" && strings.Contains(url, "qr") {
						qrCodeURL = url
						log.Printf("✅ Found QR code URL from GET method: %s", qrCodeURL)
						break
					}
				}
			}
		}
	}

	// If QR code URL not found in response but payment already has one, preserve it
	if qrCodeURL == "" && payment.QRCodeURL != "" {
		log.Printf("⚠️  QR code URL not in response, preserving existing: %s", payment.QRCodeURL)
		qrCodeURL = payment.QRCodeURL
	}

	var expiryTime *time.Time
	if expiry, ok := midtransResp["expiry_time"].(string); ok && expiry != "" {
		// Try multiple time formats
		formats := []string{
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
		}
		for _, format := range formats {
			exp, err := time.Parse(format, expiry)
			if err == nil {
				expiryTime = &exp
				break
			}
		}
	}

	webhookJSON, _ := json.Marshal(midtransResp)

	// Update payment status
	return UpdatePaymentStatus(orderID, transactionStatus, transactionID, vaNumber, bankType, qrCodeURL, expiryTime, string(webhookJSON))
}

// UpdatePaymentStatus updates payment status from Midtrans webhook
func UpdatePaymentStatus(orderID string, status string, transactionID string, vaNumber string, bankType string, qrCodeURL string, expiryTime *time.Time, midtransResponse string) error {

	paymentStatus := mapMidtransStatusToPaymentStatus(status)

	var payment Payment
	if err := db.Where("order_id = ?", orderID).First(&payment).Error; err != nil {
		log.Printf("❌ Payment not found: %v", err)
		return err
	}

	// Preserve existing QR code URL if new one is empty
	if qrCodeURL == "" && payment.QRCodeURL != "" {
		qrCodeURL = payment.QRCodeURL
	}

	// Preserve existing VA number if new one is empty
	if vaNumber == "" && payment.VANumber != "" {
		vaNumber = payment.VANumber
	}

	// Preserve existing bank type if new one is empty
	if bankType == "" && payment.BankType != "" {
		bankType = payment.BankType
	}

	updateData := map[string]interface{}{
		"status":                  paymentStatus,
		"midtrans_transaction_id": transactionID,
		"va_number":               vaNumber,
		"bank_type":               bankType,
		"qr_code_url":             qrCodeURL,
		"expiry_time":             expiryTime,
		"midtrans_response":       midtransResponse,
		"updated_at":              time.Now(),
	}

	if err := db.Model(&payment).Updates(updateData).Error; err != nil {
		log.Printf("❌ Failed to update payment: %v", err)
		return err
	}

	// Reload payment with updated data
	if err := db.Where("order_id = ?", orderID).First(&payment).Error; err != nil {
		return err
	}

	// Broadcast payment status update via WebSocket
	BroadcastPaymentStatus(&payment)

	// If payment is successful, create donation history and trigger donation
	if paymentStatus == PaymentStatusSuccess {
		// Create donation history
		historyID := uuid.New().String()
		history := DonationHistory{
			ID:        historyID,
			Type:      payment.DonationType,
			MediaURL:  payment.MediaURL,
			MediaType: payment.MediaType,
			StartTime: payment.StartTime,
			DonorName: payment.DonorName,
			Amount:    payment.Amount,
			Message:   payment.Message,
			PaymentID: &payment.ID,
			CreatedAt: time.Now(),
		}

		if err := db.Create(&history).Error; err != nil {
			log.Printf("⚠️  Failed to create donation history: %v", err)
		} else {

			// Load payment relation before broadcasting
			history.Payment = &payment

			// Broadcast history to WebSocket immediately after creation
			BroadcastHistory(&history)

			// Create donation job and publish to queue
			job := DonationJob{
				ID:        historyID,
				Type:      payment.DonationType,
				MediaURL:  payment.MediaURL,
				MediaType: payment.MediaType,
				StartTime: payment.StartTime,
				DonorName: payment.DonorName,
				Amount:    payment.Amount,
				Message:   payment.Message,
			}

			if err := PublishDonation(job); err != nil {
				log.Printf("⚠️  Failed to publish donation: %v", err)
			}
		}
	}

	return nil
}
