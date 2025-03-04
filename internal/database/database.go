package database

import (
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/ilokitv/botVPN/internal/config"
	"github.com/ilokitv/botVPN/internal/models"
)

// DB представляет соединение с базой данных
type DB struct {
	*sqlx.DB
}

// New создает новое соединение с базой данных
func New(cfg *config.DatabaseConfig) (*DB, error) {
	db, err := sqlx.Connect("postgres", cfg.GetConnectionString())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Проверка соединения
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{db}, nil
}

// InitTables создает таблицы в базе данных, если они не существуют
func (db *DB) InitTables() error {
	// Создаем таблицу для серверов
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS servers (
		id SERIAL PRIMARY KEY,
		ip TEXT NOT NULL,
		port INTEGER NOT NULL,
		ssh_user TEXT NOT NULL,
		ssh_password TEXT NOT NULL,
		max_clients INTEGER NOT NULL DEFAULT 10,
		current_clients INTEGER NOT NULL DEFAULT 0,
		is_active BOOLEAN NOT NULL DEFAULT TRUE,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	)
	`)

	if err != nil {
		return fmt.Errorf("failed to create servers table: %w", err)
	}

	// Создаем таблицу для планов подписок
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS subscription_plans (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		price REAL NOT NULL,
		duration INTEGER NOT NULL, 
		is_active BOOLEAN NOT NULL DEFAULT TRUE,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	)
	`)

	if err != nil {
		return fmt.Errorf("failed to create subscription_plans table: %w", err)
	}

	// Создаем таблицу для пользователей
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		telegram_id BIGINT NOT NULL UNIQUE,
		username TEXT,
		first_name TEXT,
		last_name TEXT,
		is_admin BOOLEAN NOT NULL DEFAULT FALSE,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	)
	`)

	if err != nil {
		return fmt.Errorf("failed to create users table: %w", err)
	}

	// Создаем таблицу для подписок
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS subscriptions (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id),
		server_id INTEGER NOT NULL REFERENCES servers(id),
		plan_id INTEGER NOT NULL REFERENCES subscription_plans(id),
		start_date TIMESTAMP NOT NULL,
		end_date TIMESTAMP NOT NULL,
		status TEXT NOT NULL,
		config_file_path TEXT,
		data_usage BIGINT NOT NULL DEFAULT 0,
		last_connection_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	)
	`)

	if err != nil {
		return fmt.Errorf("failed to create subscriptions table: %w", err)
	}

	// Создаем таблицу для платежей
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS payments (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id),
		subscription_id INTEGER REFERENCES subscriptions(id),
		amount REAL NOT NULL,
		payment_method TEXT NOT NULL,
		payment_id TEXT,
		status TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	)
	`)

	if err != nil {
		return fmt.Errorf("failed to create payments table: %w", err)
	}

	log.Println("All database tables initialized successfully")
	return nil
}

// GetServerByID возвращает сервер по ID
func (db *DB) GetServerByID(id int) (*models.Server, error) {
	var server models.Server
	err := db.Get(&server, "SELECT * FROM servers WHERE id = $1", id)
	if err != nil {
		return nil, fmt.Errorf("failed to get server by id: %w", err)
	}
	return &server, nil
}

// GetAllServers возвращает все серверы
func (db *DB) GetAllServers() ([]models.Server, error) {
	var servers []models.Server
	err := db.Select(&servers, "SELECT * FROM servers")
	if err != nil {
		return nil, fmt.Errorf("failed to get all servers: %w", err)
	}
	return servers, nil
}

// AddServer добавляет новый сервер
func (db *DB) AddServer(server *models.Server) error {
	// Валидация входных данных
	if server.IP == "" {
		return fmt.Errorf("IP сервера не может быть пустым")
	}

	if server.Port <= 0 || server.Port > 65535 {
		return fmt.Errorf("некорректный порт сервера: %d (должен быть от 1 до 65535)", server.Port)
	}

	if server.SSHUser == "" {
		return fmt.Errorf("имя пользователя SSH не может быть пустым")
	}

	if server.SSHPassword == "" {
		return fmt.Errorf("пароль SSH не может быть пустым")
	}

	if server.MaxClients <= 0 {
		return fmt.Errorf("максимальное количество клиентов должно быть положительным числом")
	}

	log.Printf("Добавление нового сервера: IP=%s, Port=%d, User=%s, MaxClients=%d",
		server.IP, server.Port, server.SSHUser, server.MaxClients)

	// Начинаем транзакцию
	tx, err := db.Beginx()
	if err != nil {
		log.Printf("Ошибка при создании транзакции: %v", err)
		return fmt.Errorf("ошибка при создании транзакции: %w", err)
	}

	// Отложенный откат транзакции в случае ошибки
	defer func() {
		if err != nil {
			log.Printf("Откат транзакции из-за ошибки: %v", err)
			tx.Rollback()
		}
	}()

	// Проверяем, существует ли уже сервер с таким IP
	var count int
	err = tx.Get(&count, "SELECT COUNT(*) FROM servers WHERE ip = $1", server.IP)
	if err != nil {
		log.Printf("Ошибка при проверке существования сервера: %v", err)
		return fmt.Errorf("ошибка при проверке существования сервера: %w", err)
	}

	if count > 0 {
		log.Printf("Сервер с IP %s уже существует", server.IP)
		return fmt.Errorf("сервер с IP %s уже существует", server.IP)
	}

	// Выполняем запрос на добавление сервера
	query := `
	INSERT INTO servers (ip, port, ssh_user, ssh_password, max_clients, current_clients, is_active, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
	RETURNING id, created_at, updated_at
	`

	row := tx.QueryRow(query, server.IP, server.Port, server.SSHUser, server.SSHPassword,
		server.MaxClients, 0, server.IsActive)

	err = row.Scan(&server.ID, &server.CreatedAt, &server.UpdatedAt)
	if err != nil {
		log.Printf("Ошибка при добавлении сервера в базу данных: %v", err)
		return fmt.Errorf("ошибка при добавлении сервера: %w", err)
	}

	// Фиксируем транзакцию
	err = tx.Commit()
	if err != nil {
		log.Printf("Ошибка при фиксации транзакции: %v", err)
		return fmt.Errorf("ошибка при фиксации транзакции: %w", err)
	}

	log.Printf("Сервер успешно добавлен с ID=%d", server.ID)
	return nil
}

// UpdateServer обновляет сервер
func (db *DB) UpdateServer(server *models.Server) error {
	query := `
	UPDATE servers
	SET ip = $1, port = $2, ssh_user = $3, ssh_password = $4, max_clients = $5,
		current_clients = $6, is_active = $7, updated_at = NOW()
	WHERE id = $8
	RETURNING updated_at
	`

	row := db.QueryRow(query, server.IP, server.Port, server.SSHUser, server.SSHPassword,
		server.MaxClients, server.CurrentClients, server.IsActive, server.ID)

	err := row.Scan(&server.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to update server: %w", err)
	}

	return nil
}

// DeleteServer удаляет сервер по ID
func (db *DB) DeleteServer(id int) error {
	_, err := db.Exec("DELETE FROM servers WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete server: %w", err)
	}
	return nil
}

// GetUserByTelegramID возвращает пользователя по ID в Telegram
func (db *DB) GetUserByTelegramID(telegramID int64) (*models.User, error) {
	var user models.User
	err := db.Get(&user, "SELECT * FROM users WHERE telegram_id = $1", telegramID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by telegram id: %w", err)
	}
	return &user, nil
}

// GetUserByID возвращает пользователя по ID в базе данных
func (db *DB) GetUserByID(userID int) (*models.User, error) {
	var user models.User
	err := db.Get(&user, "SELECT * FROM users WHERE id = $1", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}
	return &user, nil
}

// GetAllUsers возвращает всех пользователей
func (db *DB) GetAllUsers() ([]models.User, error) {
	var users []models.User

	// Добавляем отладочный вывод
	log.Println("Выполняем запрос SELECT * FROM users ORDER BY id ASC")

	err := db.Select(&users, "SELECT * FROM users ORDER BY id ASC")
	if err != nil {
		log.Printf("Ошибка при получении пользователей: %v", err)
		return nil, fmt.Errorf("failed to get all users: %w", err)
	}

	log.Printf("Запрос выполнен успешно, найдено %d пользователей", len(users))
	for i, user := range users {
		log.Printf("  Пользователь %d: ID=%d, TelegramID=%d, IsAdmin=%v",
			i+1, user.ID, user.TelegramID, user.IsAdmin)
	}

	return users, nil
}

// GetAllAdmins возвращает всех пользователей со статусом администратора
func (db *DB) GetAllAdmins() ([]models.User, error) {
	var admins []models.User
	err := db.Select(&admins, "SELECT * FROM users WHERE is_admin = TRUE ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("failed to get all admins: %w", err)
	}
	return admins, nil
}

// GetUserStats возвращает статистику для конкретного пользователя
func (db *DB) GetUserStats(userID int) (*models.UserStats, error) {
	stats := &models.UserStats{
		UserID: userID,
	}

	// Получаем количество подписок пользователя
	err := db.Get(&stats.SubscriptionsCount,
		"SELECT COUNT(*) FROM subscriptions WHERE user_id = $1", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user subscriptions count: %w", err)
	}

	// Получаем количество активных подписок
	err = db.Get(&stats.ActiveSubscriptionsCount,
		"SELECT COUNT(*) FROM subscriptions WHERE user_id = $1 AND status = 'active'", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user active subscriptions count: %w", err)
	}

	// Получаем общую сумму платежей
	err = db.Get(&stats.TotalPayments,
		"SELECT COALESCE(SUM(amount), 0) FROM payments WHERE user_id = $1 AND status = 'completed'", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user total payments: %w", err)
	}

	// Получаем общее использование данных
	err = db.Get(&stats.TotalDataUsage,
		"SELECT COALESCE(SUM(data_usage), 0) FROM subscriptions WHERE user_id = $1", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user total data usage: %w", err)
	}

	return stats, nil
}

// GetSystemStats возвращает общую статистику по системе
func (db *DB) GetSystemStats() (*models.SystemStats, error) {
	stats := &models.SystemStats{}

	// Общее количество пользователей
	err := db.Get(&stats.TotalUsers, "SELECT COUNT(*) FROM users")
	if err != nil {
		return nil, fmt.Errorf("failed to get total users count: %w", err)
	}

	// Количество активных подписок
	err = db.Get(&stats.ActiveSubscriptions, "SELECT COUNT(*) FROM subscriptions WHERE status = 'active'")
	if err != nil {
		return nil, fmt.Errorf("failed to get active subscriptions count: %w", err)
	}

	// Общий доход
	err = db.Get(&stats.TotalRevenue,
		"SELECT COALESCE(SUM(amount), 0) FROM payments WHERE status = 'completed'")
	if err != nil {
		return nil, fmt.Errorf("failed to get total revenue: %w", err)
	}

	// Доход за последний месяц
	err = db.Get(&stats.MonthlyRevenue,
		"SELECT COALESCE(SUM(amount), 0) FROM payments WHERE status = 'completed' AND created_at > NOW() - INTERVAL '30 days'")
	if err != nil {
		return nil, fmt.Errorf("failed to get monthly revenue: %w", err)
	}

	// Количество серверов
	err = db.Get(&stats.TotalServers, "SELECT COUNT(*) FROM servers WHERE is_active = TRUE")
	if err != nil {
		return nil, fmt.Errorf("failed to get total servers count: %w", err)
	}

	// Общее количество клиентов на серверах
	err = db.Get(&stats.TotalClients, "SELECT COALESCE(SUM(current_clients), 0) FROM servers")
	if err != nil {
		return nil, fmt.Errorf("failed to get total clients count: %w", err)
	}

	// Общая мощность серверов (максимальное количество клиентов)
	err = db.Get(&stats.TotalCapacity, "SELECT COALESCE(SUM(max_clients), 0) FROM servers WHERE is_active = TRUE")
	if err != nil {
		return nil, fmt.Errorf("failed to get total server capacity: %w", err)
	}

	// Регистрации за последние 7 дней
	err = db.Get(&stats.NewUsers7Days,
		"SELECT COUNT(*) FROM users WHERE created_at > NOW() - INTERVAL '7 days'")
	if err != nil {
		return nil, fmt.Errorf("failed to get new users in 7 days: %w", err)
	}

	// Новые подписки за последние 7 дней
	err = db.Get(&stats.NewSubscriptions7Days,
		"SELECT COUNT(*) FROM subscriptions WHERE created_at > NOW() - INTERVAL '7 days'")
	if err != nil {
		return nil, fmt.Errorf("failed to get new subscriptions in 7 days: %w", err)
	}

	return stats, nil
}

// SetUserAdmin устанавливает или снимает статус администратора для пользователя
func (db *DB) SetUserAdmin(userID int, isAdmin bool) error {
	_, err := db.Exec("UPDATE users SET is_admin = $1, updated_at = NOW() WHERE id = $2",
		isAdmin, userID)
	if err != nil {
		return fmt.Errorf("failed to update user admin status: %w", err)
	}
	return nil
}

// AddUser добавляет нового пользователя
func (db *DB) AddUser(user *models.User) error {
	query := `
	INSERT INTO users (telegram_id, username, first_name, last_name, is_admin)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (telegram_id) DO UPDATE
	SET username = $2, first_name = $3, last_name = $4, updated_at = NOW()
	RETURNING id, created_at, updated_at
	`

	row := db.QueryRow(query, user.TelegramID, user.Username, user.FirstName,
		user.LastName, user.IsAdmin)

	err := row.Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to add user: %w", err)
	}

	return nil
}

// GetAllSubscriptionPlans возвращает все планы подписок
func (db *DB) GetAllSubscriptionPlans() ([]models.SubscriptionPlan, error) {
	var plans []models.SubscriptionPlan
	err := db.Select(&plans, "SELECT * FROM subscription_plans WHERE is_active = TRUE")
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription plans: %w", err)
	}
	return plans, nil
}

// GetSubscriptionPlanByID возвращает план подписки по ID
func (db *DB) GetSubscriptionPlanByID(id int) (*models.SubscriptionPlan, error) {
	var plan models.SubscriptionPlan
	err := db.Get(&plan, "SELECT * FROM subscription_plans WHERE id = $1", id)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription plan by id: %w", err)
	}
	return &plan, nil
}

// AddSubscriptionPlan добавляет новый план подписки
func (db *DB) AddSubscriptionPlan(plan *models.SubscriptionPlan) error {
	query := `
	INSERT INTO subscription_plans (name, description, price, duration, is_active)
	VALUES ($1, $2, $3, $4, $5)
	RETURNING id, created_at, updated_at
	`

	row := db.QueryRow(query, plan.Name, plan.Description, plan.Price, plan.Duration, plan.IsActive)

	err := row.Scan(&plan.ID, &plan.CreatedAt, &plan.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to add subscription plan: %w", err)
	}

	return nil
}

// UpdateSubscriptionPlan обновляет план подписки
func (db *DB) UpdateSubscriptionPlan(plan *models.SubscriptionPlan) error {
	query := `
	UPDATE subscription_plans
	SET name = $1, description = $2, price = $3, duration = $4, is_active = $5, updated_at = NOW()
	WHERE id = $6
	RETURNING updated_at
	`

	row := db.QueryRow(query, plan.Name, plan.Description, plan.Price, plan.Duration, plan.IsActive, plan.ID)

	err := row.Scan(&plan.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to update subscription plan: %w", err)
	}

	return nil
}

// DeleteSubscriptionPlan удаляет план подписки (меняет флаг is_active)
func (db *DB) DeleteSubscriptionPlan(id int) error {
	_, err := db.Exec("UPDATE subscription_plans SET is_active = FALSE, updated_at = NOW() WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete subscription plan: %w", err)
	}
	return nil
}

// GetSubscriptionsByUserID возвращает все подписки пользователя
func (db *DB) GetSubscriptionsByUserID(userID int) ([]models.Subscription, error) {
	var subscriptions []models.Subscription
	err := db.Select(&subscriptions, "SELECT * FROM subscriptions WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	return subscriptions, nil
}

// GetAllSubscriptions возвращает все подписки в системе
func (db *DB) GetAllSubscriptions() ([]models.Subscription, error) {
	var subscriptions []models.Subscription
	err := db.Select(&subscriptions, "SELECT * FROM subscriptions ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	return subscriptions, nil
}

// GetSubscriptionByID возвращает подписку по её ID
func (db *DB) GetSubscriptionByID(subscriptionID int) (*models.Subscription, error) {
	var subscription models.Subscription
	err := db.Get(&subscription, "SELECT * FROM subscriptions WHERE id = $1", subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription by id: %w", err)
	}
	return &subscription, nil
}

// AddSubscription добавляет новую подписку
func (db *DB) AddSubscription(subscription *models.Subscription) error {
	query := `
	INSERT INTO subscriptions 
	(user_id, server_id, plan_id, start_date, end_date, status, config_file_path)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	RETURNING id, created_at, updated_at
	`

	row := db.QueryRow(query, subscription.UserID, subscription.ServerID, subscription.PlanID,
		subscription.StartDate, subscription.EndDate, subscription.Status, subscription.ConfigFilePath)

	err := row.Scan(&subscription.ID, &subscription.CreatedAt, &subscription.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to add subscription: %w", err)
	}

	// Обновляем счетчик клиентов на сервере
	_, err = db.Exec("UPDATE servers SET current_clients = current_clients + 1 WHERE id = $1",
		subscription.ServerID)
	if err != nil {
		return fmt.Errorf("failed to update server client count: %w", err)
	}

	return nil
}

// AddPayment добавляет новый платеж
func (db *DB) AddPayment(payment *models.Payment) error {
	query := `
	INSERT INTO payments 
	(user_id, subscription_id, amount, payment_method, payment_id, status)
	VALUES ($1, $2, $3, $4, $5, $6)
	RETURNING id, created_at, updated_at
	`

	row := db.QueryRow(query, payment.UserID, payment.SubscriptionID, payment.Amount,
		payment.PaymentMethod, payment.PaymentID, payment.Status)

	err := row.Scan(&payment.ID, &payment.CreatedAt, &payment.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to add payment: %w", err)
	}

	return nil
}

// UpdateSubscription обновляет данные подписки
func (db *DB) UpdateSubscription(subscription *models.Subscription) error {
	_, err := db.NamedExec(`
		UPDATE subscriptions SET 
		status = :status, 
		data_usage = :data_usage, 
		last_connection_at = :last_connection_at,
		updated_at = NOW()
		WHERE id = :id
	`, subscription)

	if err != nil {
		return fmt.Errorf("failed to update subscription: %w", err)
	}

	return nil
}
