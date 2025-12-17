package main

import (
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

// PaymentStatus represents payment status enum
type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "PENDING"
	PaymentStatusSuccess   PaymentStatus = "SUCCESS"
	PaymentStatusFailed    PaymentStatus = "FAILED"
	PaymentStatusCancelled PaymentStatus = "CANCELLED"
	PaymentStatusExpired   PaymentStatus = "EXPIRED"
)

// Payment represents a payment record
type Payment struct {
	ID                    string        `gorm:"primaryKey;type:varchar(36)" json:"id"`
	OrderID               string        `gorm:"type:varchar(100);uniqueIndex;not null" json:"orderId"`
	DonorName             string        `gorm:"type:varchar(255);not null" json:"donorName"`
	DonorEmail            string        `gorm:"type:varchar(255)" json:"donorEmail,omitempty"`
	Amount                int           `gorm:"type:integer;not null" json:"amount"`
	TotalAmount           int           `gorm:"type:integer;not null" json:"totalAmount"`
	Status                PaymentStatus `gorm:"type:varchar(20);default:'PENDING';index" json:"status"`
	PaymentMethod         string        `gorm:"type:varchar(50);not null" json:"paymentMethod"` // bank_transfer, gopay, credit_card
	PaymentType           string        `gorm:"type:varchar(50);default:'midtrans'" json:"paymentType"`
	DonationType          string        `gorm:"type:varchar(10);not null" json:"donationType"` // "gif" or "text"
	MediaURL              string        `gorm:"type:text" json:"mediaUrl,omitempty"`
	MediaType             string        `gorm:"type:varchar(20)" json:"mediaType,omitempty"`
	StartTime             int           `gorm:"type:integer" json:"startTime,omitempty"`
	Message               string        `gorm:"type:text" json:"message,omitempty"`
	Notes                 string        `gorm:"type:text" json:"notes,omitempty"`
	MidtransTransactionID string        `gorm:"type:varchar(100);index" json:"midtransTransactionId,omitempty"`
	FraudStatus           string        `gorm:"type:varchar(50)" json:"fraudStatus,omitempty"`
	MidtransResponse      string        `gorm:"type:text" json:"midtransResponse,omitempty"`
	MidtransAction        string        `gorm:"type:text" json:"midtransAction,omitempty"`
	VANumber              string        `gorm:"type:varchar(50)" json:"vaNumber,omitempty"`
	BankType              string        `gorm:"type:varchar(20)" json:"bankType,omitempty"`
	QRCodeURL             string        `gorm:"type:text" json:"qrCodeUrl,omitempty"`
	ExpiryTime            *time.Time    `gorm:"type:timestamp" json:"expiryTime,omitempty"`
	CreatedAt             time.Time     `gorm:"type:timestamp;default:CURRENT_TIMESTAMP;index" json:"createdAt"`
	UpdatedAt             time.Time     `gorm:"type:timestamp;default:CURRENT_TIMESTAMP" json:"updatedAt"`

	// Note: History relation is handled via History.PaymentID (one-to-one)
}

// TableName specifies the table name for GORM
func (Payment) TableName() string {
	return "payments"
}

// DonationHistory represents a donation history record
type DonationHistory struct {
	ID        string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Type      string    `gorm:"type:varchar(10);not null;check:type IN ('gif', 'text')" json:"type"` // "gif" or "text"
	MediaURL  string    `gorm:"type:text" json:"mediaUrl,omitempty"`
	MediaType string    `gorm:"type:varchar(20)" json:"mediaType,omitempty"`
	StartTime int       `gorm:"type:integer" json:"startTime,omitempty"`
	DonorName string    `gorm:"type:varchar(255);not null" json:"donorName"`
	Amount    int       `gorm:"type:integer;not null" json:"amount"`
	Message   string    `gorm:"type:text" json:"message,omitempty"`
	CreatedAt time.Time `gorm:"type:timestamp;default:CURRENT_TIMESTAMP;index:idx_donation_history_created_at" json:"createdAt"`

	// Relation to Payment
	PaymentID *string  `gorm:"type:varchar(36);index" json:"paymentId,omitempty"`
	Payment   *Payment `gorm:"foreignKey:PaymentID;references:ID" json:"payment,omitempty"`
}

// TableName specifies the table name for GORM
func (DonationHistory) TableName() string {
	return "donation_history"
}

// InitDatabase initializes PostgreSQL connection using GORM and creates tables
func InitDatabase() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://admin:admin@localhost:5432/donation_db?sslmode=disable"
	}

	var err error
	db, err = gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), // Set to logger.Info for SQL logs
	})

	if err != nil {
		return err
	}

	// Get underlying SQL DB for connection testing
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	// Test connection
	if err = sqlDB.Ping(); err != nil {
		return err
	}

	// Set connection pool settings
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Check if payments table exists and has old integer id column
	var columnType string
	err = db.Raw(`
		SELECT data_type 
		FROM information_schema.columns 
		WHERE table_name = 'payments' AND column_name = 'id'
	`).Scan(&columnType).Error

	// Check for any integer type (bigint, integer, int4, int8)
	isIntegerType := err == nil && (columnType == "bigint" || columnType == "integer" || columnType == "int4" || columnType == "int8")

	if isIntegerType {
		log.Printf("🔄 Migrating payments.id from %s to varchar(36)...", columnType)

		// Drop foreign key constraints first
		db.Exec(`
			ALTER TABLE donation_history 
			DROP CONSTRAINT IF EXISTS donation_history_payment_id_fkey
		`)

		// Check if there's existing data
		var count int64
		db.Raw("SELECT COUNT(*) FROM payments").Scan(&count)

		if count > 0 {
			log.Printf("⚠️  Found %d existing payment records. Dropping tables to migrate...", count)
			// Drop tables with CASCADE to handle foreign keys
			db.Exec("DROP TABLE IF EXISTS donation_history CASCADE")
			db.Exec("DROP TABLE IF EXISTS payments CASCADE")
		} else {
			// No data, try to alter column type
			alterErr := db.Exec(`
				ALTER TABLE payments 
				ALTER COLUMN id TYPE varchar(36)
			`).Error
			if alterErr != nil {
				log.Printf("⚠️  Failed to alter column, dropping table: %v", alterErr)
				db.Exec("DROP TABLE IF EXISTS donation_history CASCADE")
				db.Exec("DROP TABLE IF EXISTS payments CASCADE")
			} else {
				// Successfully altered, now alter donation_history
				db.Exec(`
					ALTER TABLE donation_history 
					ALTER COLUMN payment_id TYPE varchar(36)
				`)
			}
		}
	}

	// Auto migrate - creates tables and indexes
	err = db.AutoMigrate(&Payment{}, &DonationHistory{})
	if err != nil {
		return err
	}

	// Create additional indexes if needed
	db.Exec("CREATE INDEX IF NOT EXISTS idx_donation_history_type ON donation_history(type)")

	log.Println("✅ Database initialized successfully with GORM")
	return nil
}

// SaveDonationHistory saves a donation to history (only for gif and text types)
func SaveDonationHistory(job DonationJob) error {
	// Only save gif and text donations
	if job.Type != "gif" && job.Type != "text" {
		return nil // Skip other types silently
	}

	// Generate UUID if not provided
	if job.ID == "" {
		job.ID = uuid.New().String()
	}

	history := DonationHistory{
		ID:        job.ID,
		Type:      job.Type,
		MediaURL:  job.MediaURL,
		MediaType: job.MediaType,
		StartTime: job.StartTime,
		DonorName: job.DonorName,
		Amount:    job.Amount,
		Message:   job.Message,
		CreatedAt: time.Now(),
	}

	if err := db.Create(&history).Error; err != nil {
		log.Printf("❌ Failed to save donation history: %v", err)
		return err
	}

	log.Printf("✅ Donation history saved: %s (%s) - %s - Rp%d", job.ID, job.Type, job.DonorName, job.Amount)
	return nil
}

// GetDonationHistory retrieves donation history with pagination
func GetDonationHistory(limit, offset int) ([]DonationHistory, error) {
	var history []DonationHistory

	err := db.Order("created_at DESC").
		Preload("Payment"). // Preload payment relation to get payment method info
		Limit(limit).
		Offset(offset).
		Find(&history).Error

	if err != nil {
		return nil, err
	}

	return history, nil
}

// GetDonationHistoryByID retrieves a single donation history by ID
func GetDonationHistoryByID(id string) (*DonationHistory, error) {
	var history DonationHistory

	err := db.Where("id = ?", id).First(&history).Error
	if err != nil {
		return nil, err
	}

	return &history, nil
}
