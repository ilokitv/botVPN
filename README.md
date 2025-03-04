# Telegram VPN Bot

## Описание проекта

Telegram VPN Bot - это полнофункциональное решение для автоматизации продажи подписок на VPN сервис с использованием протокола WireGuard. Бот позволяет пользователям покупать VPN-подписки прямо в Telegram, а администраторам - управлять серверами, клиентами и подписками.

## Основные возможности

### Для пользователей:
- 🛒 Покупка VPN-подписок разной длительности
- 📊 Просмотр статуса своих активных подписок
- 📁 Автоматическое получение файла конфигурации VPN
- 💳 Удобная оплата через встроенные платежи Telegram
- 📆 Уведомления о статусе подписки и её окончании

### Для администраторов:
- 🖥️ Управление VPN-серверами (добавление, редактирование, удаление)
- 👥 Управление пользователями (просмотр, блокировка, добавление привилегий)
- 📋 Управление планами подписок (создание, изменение, удаление)
- 📈 Просмотр статистики продаж и использования
- 📢 Массовая рассылка уведомлений пользователям

## Технические требования

- 🔷 Go 1.21 или выше
- 📦 PostgreSQL 13 или выше
- 🔒 WireGuard (установленный на VPN-сервере)
- 🌐 SSH доступ к серверу для настройки VPN
- 🤖 Зарегистрированный Telegram бот (через @BotFather)

## Установка и настройка

### 1. Клонирование репозитория

```bash
git clone https://github.com/ilokitv/botVPN.git
cd botVPN
```

### 2. Установка зависимостей

```bash
go mod download
```

### 3. Настройка базы данных

1. Создайте базу данных PostgreSQL:

```sql
CREATE DATABASE vpnbot;
```

2. Скопируйте пример конфигурационного файла и настройте его:

```bash
cp config.yaml.example config.yaml
```

3. Отредактируйте файл `config.yaml`, указав свои параметры подключения к базе данных и Telegram-боту:

```yaml
bot:
  token: "ВАШ_ТОКЕН_БОТА"  # Получите у @BotFather
  admin_ids: [123456789]   # ID администраторов (можно получить у @userinfobot)

database:
  host: "localhost"        # Адрес сервера базы данных
  port: 5432               # Порт PostgreSQL
  user: "postgres"         # Имя пользователя
  password: "password"     # Пароль
  dbname: "vpnbot"         # Имя базы данных
  sslmode: "disable"       # Режим SSL

payments:
  provider: "123456789:TEST:abcdefghijklmnopqrstuvwxyz"  # Токен для платежей Telegram
```

### 4. Сборка проекта

```bash
go build -o vpnbot cmd/bot/main.go
```

### 5. Настройка WireGuard на VPN-сервере

1. Установите WireGuard на ваш сервер:

```bash
# Для Debian/Ubuntu
sudo apt update
sudo apt install wireguard

# Для CentOS/RHEL
sudo yum install wireguard
```

2. Настройте переадресацию IP и параметры сети на сервере:

```bash
# Включение IP-форвардинга
echo "net.ipv4.ip_forward = 1" | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Настройка правил файрвола (если используется iptables)
sudo iptables -A FORWARD -i wg0 -j ACCEPT
sudo iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
```

## Запуск и управление

### Запуск бота

```bash
./vpnbot
```

Или с указанием пути к конфигурационному файлу:

```bash
./vpnbot -config path/to/config.yaml
```

### Запуск в фоновом режиме (демон)

```bash
nohup ./vpnbot > vpnbot.log 2>&1 &
echo $! > vpnbot.pid
```

### Остановка бота

```bash
kill $(cat vpnbot.pid)
```

## Использование бота

### Команды для пользователей:

- `/start` - начать работу с ботом и открыть главное меню
- `/help` - показать справку по командам
- `/buy` - выбрать и купить подписку на VPN
- `/my` - просмотреть активные подписки и их статус
- `/config` - получить файл конфигурации для активной подписки

### Команды для администраторов:

- `/admin` - открыть меню администратора
  - Управление серверами
  - Управление пользователями
  - Управление тарифами
  - Статистика и отчеты


## Структура проекта

```
botVPN/
├── cmd/                     # Исполняемые файлы
│   └── bot/                 # Основное приложение бота
│       └── main.go          # Точка входа
├── internal/                # Внутренние пакеты
│   ├── config/              # Работа с конфигурацией
│   ├── database/            # Взаимодействие с базой данных
│   ├── handlers/            # Обработчики команд и сообщений
│   ├── models/              # Модели данных
│   ├── scheduler/           # Планировщик задач
│   ├── services/            # Бизнес-логика
│   └── vpn/                 # Управление VPN и конфигурациями
├── scripts/                 # Вспомогательные скрипты
│   ├── install.sh           # Скрипт установки
│   └── backup.sh            # Резервное копирование
├── vpn_configs/             # Директория для файлов конфигурации VPN
├── temp/                    # Временные файлы
├── bin/                     # Скомпилированные бинарные файлы
├── config.yaml              # Файл конфигурации
├── config.yaml.example      # Пример конфигурационного файла
├── go.mod                   # Зависимости Go
├── go.sum                   # Контрольные суммы зависимостей
└── README.md                # Документация проекта
```

## Безопасность

- Все данные пользователей и конфигурации хранятся в защищенном виде
- Для каждого клиента создается отдельная конфигурация
- Администраторский доступ защищен верификацией Telegram ID
- Используется современный и безопасный протокол WireGuard

## Особенности реализации

- Полностью асинхронная обработка запросов
- Автоматическая генерация конфигураций WireGuard
- Планировщик для проверки и обновления статуса подписок
- Система уведомлений об истечении срока подписки





---

© 2024 ilokitv 
