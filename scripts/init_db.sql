-- Создаем таблицу для серверов
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
);

-- Создаем таблицу для планов подписок
CREATE TABLE IF NOT EXISTS subscription_plans (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    price REAL NOT NULL,
    duration INTEGER NOT NULL, 
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Создаем таблицу для пользователей
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL UNIQUE,
    username TEXT,
    first_name TEXT,
    last_name TEXT,
    is_admin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Создаем таблицу для подписок
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
);

-- Создаем таблицу для платежей
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
);

-- Добавляем тестовые данные
INSERT INTO subscription_plans (name, description, price, duration) VALUES
('Базовый', 'Базовый план на 1 месяц', 299.0, 30),
('Стандарт', 'Стандартный план на 3 месяца', 799.0, 90),
('Премиум', 'Премиум план на 12 месяцев', 2499.0, 365); 