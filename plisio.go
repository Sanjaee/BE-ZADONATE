package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	PLISIO_API_KEY     = "K1E90c5WRly4i69szH9xjkUF-0rDM-tl3WKA06hMayTBDvuOmjjsj3z_i_f7NIFk"
	PLISIO_BASE_URL    = "https://api.plisio.net/api/v1"
	PLISIO_API_KEY_ENV = "PLISIO_API_KEY" // Environment variable name
)

// PlisioCreateInvoiceRequest represents request to create Plisio invoice
type PlisioCreateInvoiceRequest struct {
	DonorName    string `json:"donorName" binding:"required"`
	DonorEmail   string `json:"donorEmail"`
	Amount       int    `json:"amount" binding:"required,min=1"` // Amount in USD cents (will be converted to crypto)
	Currency     string `json:"currency,omitempty"`              // Crypto currency (BTC, ETH, etc.) - optional, if empty Plisio will auto-select
	DonationType string `json:"donationType" binding:"required,oneof=gif text"`
	MediaURL     string `json:"mediaUrl,omitempty"`
	MediaType    string `json:"mediaType,omitempty"`
	StartTime    int    `json:"startTime,omitempty"`
	Message      string `json:"message,omitempty"`
	Notes        string `json:"notes,omitempty"`
	ExpireMin    int    `json:"expireMin,omitempty"` // Invoice expiry in minutes
}

// PlisioInvoiceResponse represents Plisio invoice creation response
type PlisioInvoiceResponse struct {
	Status string      `json:"status"`
	Data   interface{} `json:"data,omitempty"` // Can be PlisioInvoiceData or PlisioError
}

// PlisioInvoiceData represents invoice data from Plisio
type PlisioInvoiceData struct {
	TxnID                 string `json:"txn_id"`
	InvoiceURL            string `json:"invoice_url"`
	InvoiceTotalSum       string `json:"invoice_total_sum,omitempty"`
	Amount                string `json:"amount,omitempty"`                 // White-label
	PendingAmount         string `json:"pending_amount,omitempty"`         // White-label
	WalletHash            string `json:"wallet_hash,omitempty"`            // White-label
	PsysCid               string `json:"psys_cid,omitempty"`               // White-label
	Currency              string `json:"currency,omitempty"`               // White-label
	Status                string `json:"status,omitempty"`                 // White-label
	SourceCurrency        string `json:"source_currency,omitempty"`        // White-label
	SourceRate            string `json:"source_rate,omitempty"`            // White-label
	ExpireUtc             int64  `json:"expire_utc,omitempty"`             // White-label
	ExpectedConfirmations string `json:"expected_confirmations,omitempty"` // White-label
	QRCode                string `json:"qr_code,omitempty"`                // White-label (base64)
	VerifyHash            string `json:"verify_hash,omitempty"`            // White-label
	InvoiceCommission     string `json:"invoice_commission,omitempty"`     // White-label
	InvoiceSum            string `json:"invoice_sum,omitempty"`            // White-label
}

// PlisioError represents error response from Plisio
type PlisioError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// PlisioCallbackData represents callback data from Plisio webhook
type PlisioCallbackData struct {
	TxnID             string `json:"txn_id"`
	IpnType           string `json:"ipn_type"`
	Merchant          string `json:"merchant"`
	MerchantID        string `json:"merchant_id"`
	Amount            string `json:"amount"`
	Currency          string `json:"currency"`
	OrderNumber       string `json:"order_number"`
	OrderName         string `json:"order_name"`
	Confirmations     string `json:"confirmations"`
	Status            string `json:"status"`
	SourceCurrency    string `json:"source_currency,omitempty"`
	SourceAmount      string `json:"source_amount,omitempty"`
	SourceRate        string `json:"source_rate,omitempty"`
	Comment           string `json:"comment,omitempty"`
	VerifyHash        string `json:"verify_hash"`
	InvoiceCommission string `json:"invoice_commission,omitempty"`
	InvoiceSum        string `json:"invoice_sum,omitempty"`
	InvoiceTotalSum   string `json:"invoice_total_sum,omitempty"`
	SwitchID          string `json:"switch_id,omitempty"`
	PsysCid           string `json:"psys_cid,omitempty"`       // White-label
	PendingAmount     string `json:"pending_amount,omitempty"` // White-label
	QRCode            string `json:"qr_code,omitempty"`        // White-label
	TxURLs            string `json:"tx_urls,omitempty"`        // White-label
	ExpireUtc         string `json:"expire_utc,omitempty"`
}

// PlisioCurrency represents supported cryptocurrency
type PlisioCurrency struct {
	Name                        string `json:"name"`
	Cid                         string `json:"cid"`
	Currency                    string `json:"currency"`
	Icon                        string `json:"icon"`
	RateUsd                     string `json:"rate_usd"`
	PriceUsd                    string `json:"price_usd"`
	Precision                   string `json:"precision"` // Can be number or string from API
	Fiat                        string `json:"fiat"`
	FiatRate                    string `json:"fiat_rate"`
	MinSumIn                    string `json:"min_sum_in"`
	InvoiceCommissionPercentage string `json:"invoice_commission_percentage"`
	Hidden                      int    `json:"hidden"`
	Maintenance                 bool   `json:"maintenance"`
}

// UnmarshalJSON custom unmarshaler to handle precision and other fields as number or string
func (c *PlisioCurrency) UnmarshalJSON(data []byte) error {
	// Use map to parse all fields flexibly
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Helper function to convert to string
	toString := func(v interface{}) string {
		switch val := v.(type) {
		case string:
			return val
		case float64:
			return strconv.FormatFloat(val, 'f', -1, 64)
		case int:
			return strconv.Itoa(val)
		case int64:
			return strconv.FormatInt(val, 10)
		default:
			return fmt.Sprintf("%v", val)
		}
	}

	// Set all fields with type conversion
	if v, ok := raw["name"]; ok {
		c.Name = toString(v)
	}
	if v, ok := raw["cid"]; ok {
		c.Cid = toString(v)
	}
	if v, ok := raw["currency"]; ok {
		c.Currency = toString(v)
	}
	if v, ok := raw["icon"]; ok {
		c.Icon = toString(v)
	}
	if v, ok := raw["rate_usd"]; ok {
		c.RateUsd = toString(v)
	}
	if v, ok := raw["price_usd"]; ok {
		c.PriceUsd = toString(v)
	}
	if v, ok := raw["precision"]; ok {
		c.Precision = toString(v)
	}
	if v, ok := raw["fiat"]; ok {
		c.Fiat = toString(v)
	}
	if v, ok := raw["fiat_rate"]; ok {
		c.FiatRate = toString(v)
	}
	if v, ok := raw["min_sum_in"]; ok {
		c.MinSumIn = toString(v)
	}
	if v, ok := raw["invoice_commission_percentage"]; ok {
		c.InvoiceCommissionPercentage = toString(v)
	}
	if v, ok := raw["hidden"]; ok {
		if f, ok := v.(float64); ok {
			c.Hidden = int(f)
		} else if i, ok := v.(int); ok {
			c.Hidden = i
		}
	}
	if v, ok := raw["maintenance"]; ok {
		if b, ok := v.(bool); ok {
			c.Maintenance = b
		}
	}

	return nil
}

// PlisioCurrenciesResponse represents response for supported currencies
type PlisioCurrenciesResponse struct {
	Status string      `json:"status"`
	Data   interface{} `json:"data,omitempty"` // Can be []PlisioCurrency or PlisioError
}

// getPlisioAPIKey returns Plisio API key from environment or constant
func getPlisioAPIKey() string {
	key := os.Getenv(PLISIO_API_KEY_ENV)
	if key != "" {
		return key
	}
	return PLISIO_API_KEY
}

// CreatePlisioInvoice creates an invoice in Plisio
func CreatePlisioInvoice(req PlisioCreateInvoiceRequest) (*Payment, *PlisioInvoiceData, error) {
	// Generate order ID and order number (use UUID without prefix for order_number)
	orderID := fmt.Sprintf("DONATE_%s", uuid.New().String())
	orderNumber := uuid.New().String() // Use UUID for unique order number

	// Always use USD as source currency for conversion to crypto
	// Convert amount from cents to dollars
	sourceAmount := float64(req.Amount) / 100.0 // Convert cents to dollars

	// Validate minimum amount if currency is specified
	if req.Currency != "" {
		currencies, err := GetPlisioCurrencies("")
		if err == nil {
			for _, crypto := range currencies {
				if crypto.Cid == req.Currency {
					// Parse min_sum_in (minimum amount in crypto)
					minSumIn, err := strconv.ParseFloat(crypto.MinSumIn, 64)
					if err == nil {
						// Parse fiat_rate (rate from crypto to USD)
						fiatRate, err := strconv.ParseFloat(crypto.FiatRate, 64)
						if err == nil && fiatRate > 0 {
							// Calculate minimum USD amount needed
							// minSumIn (crypto) * fiatRate = minimum USD
							minUsdAmount := minSumIn / fiatRate
							// Add 10% buffer for commission and rate fluctuations
							minUsdAmount = minUsdAmount * 1.1

							if sourceAmount < minUsdAmount {
								return nil, nil, fmt.Errorf("amount too small. minimum amount for %s is approximately $%.2f USD (%.6f %s)",
									crypto.Name, minUsdAmount, minSumIn, crypto.Currency)
							}
							log.Printf("✅ Amount validation passed: $%.2f >= $%.2f (min for %s)", sourceAmount, minUsdAmount, crypto.Name)
						}
					}
					break
				}
			}
		}
	} else {
		// If no currency specified, enforce minimum $1 USD
		if sourceAmount < 1.0 {
			return nil, nil, fmt.Errorf("amount too small. minimum amount is $1.00 USD")
		}
	}

	// Get API key
	apiKey := getPlisioAPIKey()

	// Build URL with query parameters
	baseURL := fmt.Sprintf("%s/invoices/new", PLISIO_BASE_URL)
	params := url.Values{}
	params.Add("api_key", apiKey)
	params.Add("order_number", orderNumber)
	params.Add("order_name", fmt.Sprintf("Donation from %s", req.DonorName))

	params.Add("source_currency", "USD")
	params.Add("source_amount", fmt.Sprintf("%.2f", sourceAmount))

	// Optional: specific crypto currency
	if req.Currency != "" {
		params.Add("currency", req.Currency)
	}

	// Email
	if req.DonorEmail != "" {
		params.Add("email", req.DonorEmail)
	}

	// Callback URL (point to backend, not frontend)
	backendURL := os.Getenv("BACKEND_URL")
	if backendURL == "" {
		backendURL = os.Getenv("FRONTEND_URL")
		if backendURL == "" {
			backendURL = "http://localhost:5000"
		}
	}
	callbackURL := fmt.Sprintf("%s/payment/plisio/webhook?json=true", backendURL)
	params.Add("callback_url", callbackURL)
	log.Printf("🔗 Plisio callback URL: %s", callbackURL)

	// Success and fail callback URLs
	successCallbackURL := fmt.Sprintf("%s/payment/plisio/webhook?json=true&type=success", backendURL)
	failCallbackURL := fmt.Sprintf("%s/payment/plisio/webhook?json=true&type=fail", backendURL)
	params.Add("success_callback_url", successCallbackURL)
	params.Add("fail_callback_url", failCallbackURL)
	log.Printf("🔗 Plisio success callback URL: %s", successCallbackURL)
	log.Printf("🔗 Plisio fail callback URL: %s", failCallbackURL)

	// Expiry time (default 24 hours = 1440 minutes)
	if req.ExpireMin == 0 {
		req.ExpireMin = 1440
	}
	params.Add("expire_min", strconv.Itoa(req.ExpireMin))

	// Description
	description := fmt.Sprintf("Donation %s", req.DonationType)
	if req.Message != "" {
		description += fmt.Sprintf(": %s", req.Message)
	}
	params.Add("description", description)

	// Build full URL
	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	log.Printf("🔗 Creating Plisio invoice: %s", fullURL)

	// Create payment record first
	paymentID := uuid.New().String()
	// Store order_number in Notes field for lookup (format: "plisio_order_number:UUID")
	notesWithOrderNumber := fmt.Sprintf("plisio_order_number:%s", orderNumber)
	if req.Notes != "" {
		notesWithOrderNumber = fmt.Sprintf("%s | %s", notesWithOrderNumber, req.Notes)
	}

	payment := Payment{
		ID:            paymentID,
		OrderID:       orderID,
		DonorName:     req.DonorName,
		DonorEmail:    req.DonorEmail,
		Amount:        req.Amount,
		TotalAmount:   req.Amount,
		Status:        PaymentStatusPending,
		PaymentMethod: "crypto",
		PaymentType:   "plisio",
		DonationType:  req.DonationType,
		MediaURL:      req.MediaURL,
		MediaType:     req.MediaType,
		StartTime:     req.StartTime,
		Message:       req.Message,
		Notes:         notesWithOrderNumber,
	}

	if err := db.Create(&payment).Error; err != nil {
		log.Printf("❌ Failed to create payment: %v", err)
		return nil, nil, fmt.Errorf("failed to create payment: %v", err)
	}

	// Make GET request to Plisio API
	httpReq, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %v", err)
	}

	httpReq.Header.Set("Accept", "application/json")

	// Increase timeout to 60 seconds for Plisio API
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("⚠️  Failed to create Plisio invoice: %v", err)
		return &payment, nil, fmt.Errorf("failed to call Plisio API: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("⚠️  Failed to read Plisio response: %v", err)
		return &payment, nil, fmt.Errorf("failed to read response: %v", err)
	}

	log.Printf("📥 Plisio response (status %d): %s", resp.StatusCode, string(body))

	// Check HTTP status code
	if resp.StatusCode != 200 {
		log.Printf("❌ Plisio API returned status %d: %s", resp.StatusCode, string(body))
		return &payment, nil, fmt.Errorf("plisio API returned status %d: %s", resp.StatusCode, string(body))
	}

	var plisioResp PlisioInvoiceResponse
	if err := json.Unmarshal(body, &plisioResp); err != nil {
		log.Printf("❌ Failed to parse Plisio response: %v", err)
		return &payment, nil, fmt.Errorf("failed to parse response: %v", err)
	}

	if plisioResp.Status != "success" {
		// Try to parse error
		errorMsg := "Unknown error"
		if errorData, ok := plisioResp.Data.(map[string]interface{}); ok {
			if msg, ok := errorData["message"].(string); ok {
				errorMsg = msg
			}
		}
		log.Printf("❌ Plisio API error: %s", errorMsg)
		return &payment, nil, fmt.Errorf("plisio API error: %s", errorMsg)
	}

	// Parse invoice data
	var invoiceData PlisioInvoiceData
	invoiceDataBytes, _ := json.Marshal(plisioResp.Data)
	if err := json.Unmarshal(invoiceDataBytes, &invoiceData); err != nil {
		log.Printf("❌ Failed to parse invoice data: %v", err)
		return &payment, nil, fmt.Errorf("failed to parse invoice data: %v", err)
	}

	// Update payment with Plisio response
	updateData := map[string]interface{}{
		"midtrans_transaction_id": invoiceData.TxnID, // Reuse this field for Plisio txn_id
		"qr_code_url":             invoiceData.InvoiceURL,
		"midtrans_response":       string(body),
		"updated_at":              time.Now(),
	}

	// Parse expiry time if available
	if invoiceData.ExpireUtc > 0 {
		expiryTime := time.Unix(invoiceData.ExpireUtc, 0)
		updateData["expiry_time"] = &expiryTime
	}

	if err := db.Model(&payment).Updates(updateData).Error; err != nil {
		log.Printf("⚠️  Failed to update payment: %v", err)
	} else {
		log.Printf("✅ Payment updated with Plisio invoice data")
	}

	log.Printf("✅ Plisio invoice created: %s - %s - USD %d cents", orderID, req.DonorName, req.Amount)

	return &payment, &invoiceData, nil
}

// VerifyPlisioCallback verifies Plisio callback data using HMAC SHA1
func VerifyPlisioCallback(data map[string]interface{}) bool {
	verifyHash, ok := data["verify_hash"].(string)
	if !ok || verifyHash == "" {
		log.Printf("❌ Missing verify_hash in callback data")
		return false
	}

	apiKey := getPlisioAPIKey()
	if apiKey == "" {
		log.Printf("❌ Plisio API key not configured")
		return false
	}

	// Create a copy of data without verify_hash
	orderedData := make(map[string]interface{})
	for k, v := range data {
		if k != "verify_hash" {
			orderedData[k] = v
		}
	}

	// Sort keys
	keys := make([]string, 0, len(orderedData))
	for k := range orderedData {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build string representation (similar to PHP serialize)
	// Note: parts variable is not used in Go implementation, we use JSON instead
	_ = keys // keys are used for sorting, but we use JSON stringification approach

	// For Go, we'll use JSON stringification approach (as per Node.js example)
	jsonBytes, err := json.Marshal(orderedData)
	if err != nil {
		log.Printf("❌ Failed to marshal callback data: %v", err)
		return false
	}

	// Calculate HMAC SHA1
	mac := hmac.New(sha1.New, []byte(apiKey))
	mac.Write(jsonBytes)
	calculatedHash := hex.EncodeToString(mac.Sum(nil))

	isValid := calculatedHash == verifyHash
	if !isValid {
		log.Printf("❌ Invalid callback hash. Expected: %s, Got: %s", verifyHash, calculatedHash)
		log.Printf("📋 Callback data: %s", string(jsonBytes))
	}

	return isValid
}

// mapPlisioStatusToPaymentStatus maps Plisio status to PaymentStatus
func mapPlisioStatusToPaymentStatus(status string) PaymentStatus {
	switch strings.ToLower(status) {
	case "new":
		return PaymentStatusPending
	case "pending", "pending internal":
		return PaymentStatusPending
	case "completed":
		return PaymentStatusSuccess
	case "expired":
		return PaymentStatusExpired
	case "cancelled", "cancelled duplicate":
		return PaymentStatusCancelled
	case "error":
		return PaymentStatusFailed
	default:
		return PaymentStatusPending
	}
}

// UpdatePaymentStatusFromPlisio updates payment status from Plisio webhook
func UpdatePaymentStatusFromPlisio(callbackData PlisioCallbackData) error {
	log.Printf("🔄 Updating payment status from Plisio: OrderNumber=%s, Status=%s", callbackData.OrderNumber, callbackData.Status)

	paymentStatus := mapPlisioStatusToPaymentStatus(callbackData.Status)

	// Find payment by order number (stored in Notes field)
	var payment Payment
	orderNumberPattern := fmt.Sprintf("plisio_order_number:%s", callbackData.OrderNumber)

	// First try to find by order_number stored in Notes
	if err := db.Where("notes LIKE ?", "%"+orderNumberPattern+"%").First(&payment).Error; err != nil {
		// If not found, try by txn_id (Plisio transaction ID)
		if callbackData.TxnID != "" {
			if err := db.Where("midtrans_transaction_id = ?", callbackData.TxnID).First(&payment).Error; err != nil {
				log.Printf("❌ Payment not found for OrderNumber: %s, TxnID: %s", callbackData.OrderNumber, callbackData.TxnID)
				return fmt.Errorf("payment not found for order_number: %s", callbackData.OrderNumber)
			}
			log.Printf("✅ Found payment by txn_id: %s", callbackData.TxnID)
		} else {
			log.Printf("❌ Payment not found for OrderNumber: %s", callbackData.OrderNumber)
			return fmt.Errorf("payment not found for order_number: %s", callbackData.OrderNumber)
		}
	} else {
		log.Printf("✅ Found payment by order_number: %s", callbackData.OrderNumber)
	}

	// Parse expiry time if available
	var expiryTime *time.Time
	if callbackData.ExpireUtc != "" {
		if expireInt, err := strconv.ParseInt(callbackData.ExpireUtc, 10, 64); err == nil {
			exp := time.Unix(expireInt, 0)
			expiryTime = &exp
		}
	}

	// Convert callback data to JSON for storage
	callbackJSON, _ := json.Marshal(callbackData)

	updateData := map[string]interface{}{
		"status":                  paymentStatus,
		"midtrans_transaction_id": callbackData.TxnID,  // Reuse this field
		"qr_code_url":             callbackData.QRCode, // Store QR code if available
		"expiry_time":             expiryTime,
		"midtrans_response":       string(callbackJSON),
		"updated_at":              time.Now(),
	}

	// Check if status actually changed
	oldStatus := payment.Status
	if err := db.Model(&payment).Updates(updateData).Error; err != nil {
		log.Printf("❌ Failed to update payment: %v", err)
		return err
	}

	// Reload payment with updated data
	if err := db.Where("id = ?", payment.ID).First(&payment).Error; err != nil {
		log.Printf("❌ Failed to reload payment: %v", err)
		return err
	}

	log.Printf("✅ Payment status updated: %s -> %s (OrderID: %s)", oldStatus, payment.Status, payment.OrderID)

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
			log.Printf("✅ Donation history created: %s - %s - Rp%d", historyID, payment.DonorName, payment.Amount)

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

// GetPlisioCurrencies fetches supported cryptocurrencies from Plisio
// Always uses endpoint without fiat to get all currencies
func GetPlisioCurrencies(sourceCurrency string) ([]PlisioCurrency, error) {
	apiKey := getPlisioAPIKey()
	// Always use endpoint without fiat parameter to get all currencies
	baseURL := fmt.Sprintf("%s/currencies", PLISIO_BASE_URL)
	fullURL := fmt.Sprintf("%s?api_key=%s", baseURL, apiKey)

	log.Printf("🔗 Fetching Plisio currencies: %s", fullURL)

	httpReq, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	httpReq.Header.Set("Accept", "application/json")

	// Increase timeout to 60 seconds
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("❌ Failed to call Plisio API: %v", err)
		return nil, fmt.Errorf("failed to call Plisio API: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("❌ Failed to read response: %v", err)
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	log.Printf("📥 Plisio currencies response (status %d): %s", resp.StatusCode, string(body))

	// Check HTTP status code
	if resp.StatusCode != 200 {
		log.Printf("❌ Plisio API returned status %d: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("plisio API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response to check status first
	var responseWrapper struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &responseWrapper); err != nil {
		log.Printf("❌ Failed to parse response: %v, body: %s", err, string(body))
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	if responseWrapper.Status != "success" {
		// Try to parse error
		var errorData struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			Code    int    `json:"code"`
		}
		if err := json.Unmarshal(responseWrapper.Data, &errorData); err == nil {
			errorMsg := errorData.Message
			if errorMsg == "" {
				errorMsg = errorData.Name
			}
			if errorMsg == "" {
				errorMsg = "Unknown error"
			}
			log.Printf("❌ Plisio API error: %s", errorMsg)
			return nil, fmt.Errorf("plisio API error: %s", errorMsg)
		}
		log.Printf("❌ Plisio API error: %s", string(responseWrapper.Data))
		return nil, fmt.Errorf("plisio API error: %s", string(responseWrapper.Data))
	}

	// Parse currencies data directly from raw JSON - this will trigger custom UnmarshalJSON
	var currencies []PlisioCurrency
	if err := json.Unmarshal(responseWrapper.Data, &currencies); err != nil {
		log.Printf("❌ Failed to parse currencies data: %v, data: %s", err, string(responseWrapper.Data))
		return nil, fmt.Errorf("failed to parse currencies data: %v", err)
	}

	log.Printf("✅ Fetched %d cryptocurrencies from Plisio", len(currencies))
	return currencies, nil
}
