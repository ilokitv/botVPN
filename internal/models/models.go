package models

import "time"

// Server представляет VPN-сервер
type Server struct {
	ID             int       `db:"id" json:"id"`
	IP             string    `db:"ip" json:"ip"`
	Port           int       `db:"port" json:"port"`
	SSHUser        string    `db:"ssh_user" json:"ssh_user"`
	SSHPassword    string    `db:"ssh_password" json:"-"`
	MaxClients     int       `db:"max_clients" json:"max_clients"`
	CurrentClients int       `db:"current_clients" json:"current_clients"`
	IsActive       bool      `db:"is_active" json:"is_active"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
}

// SubscriptionPlan представляет план подписки
type SubscriptionPlan struct {
	ID          int       `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	Price       float64   `db:"price" json:"price"`
	Duration    int       `db:"duration" json:"duration"` // Длительность в днях
	IsActive    bool      `db:"is_active" json:"is_active"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// User представляет пользователя бота
type User struct {
	ID         int       `db:"id" json:"id"`
	TelegramID int64     `db:"telegram_id" json:"telegram_id"`
	Username   string    `db:"username" json:"username"`
	FirstName  string    `db:"first_name" json:"first_name"`
	LastName   string    `db:"last_name" json:"last_name"`
	IsAdmin    bool      `db:"is_admin" json:"is_admin"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

// Subscription представляет подписку пользователя
type Subscription struct {
	ID               int        `db:"id" json:"id"`
	UserID           int        `db:"user_id" json:"user_id"`
	ServerID         int        `db:"server_id" json:"server_id"`
	PlanID           int        `db:"plan_id" json:"plan_id"`
	StartDate        time.Time  `db:"start_date" json:"start_date"`
	EndDate          time.Time  `db:"end_date" json:"end_date"`
	Status           string     `db:"status" json:"status"` // active, expired, cancelled
	ConfigFilePath   string     `db:"config_file_path" json:"-"`
	DataUsage        int64      `db:"data_usage" json:"data_usage"` // Использование данных в байтах
	LastConnectionAt *time.Time `db:"last_connection_at" json:"last_connection_at"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at" json:"updated_at"`
}

// Payment представляет платеж пользователя
type Payment struct {
	ID             int       `db:"id" json:"id"`
	UserID         int       `db:"user_id" json:"user_id"`
	SubscriptionID int       `db:"subscription_id" json:"subscription_id"`
	Amount         float64   `db:"amount" json:"amount"`
	PaymentMethod  string    `db:"payment_method" json:"payment_method"` // telegram_stars
	PaymentID      string    `db:"payment_id" json:"payment_id"`
	Status         string    `db:"status" json:"status"` // pending, completed, failed
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
}

// UserStats представляет статистику для конкретного пользователя
type UserStats struct {
	UserID                   int     `json:"user_id"`
	SubscriptionsCount       int     `json:"subscriptions_count"`
	ActiveSubscriptionsCount int     `json:"active_subscriptions_count"`
	TotalPayments            float64 `json:"total_payments"`
	TotalDataUsage           int64   `json:"total_data_usage"`
}

// SystemStats представляет общую статистику по системе
type SystemStats struct {
	TotalUsers            int     `json:"total_users"`
	ActiveSubscriptions   int     `json:"active_subscriptions"`
	TotalRevenue          float64 `json:"total_revenue"`
	MonthlyRevenue        float64 `json:"monthly_revenue"`
	TotalServers          int     `json:"total_servers"`
	TotalClients          int     `json:"total_clients"`
	TotalCapacity         int     `json:"total_capacity"`
	NewUsers7Days         int     `json:"new_users_7days"`
	NewSubscriptions7Days int     `json:"new_subscriptions_7days"`
}
