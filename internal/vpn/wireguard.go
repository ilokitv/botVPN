package vpn

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ilokitv/botVPN/internal/models"
)

// WireguardManager управляет VPN сервером Wireguard
type WireguardManager struct {
	ConfigDir string // Директория для хранения файлов конфигурации
}

// NewWireguardManager создает нового менеджера Wireguard
func NewWireguardManager(configDir string) *WireguardManager {
	// Создаем директорию, если она не существует
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		err := os.MkdirAll(configDir, 0755)
		if err != nil {
			log.Printf("Error creating config directory: %v", err)
		}
	}

	return &WireguardManager{
		ConfigDir: configDir,
	}
}

// SetupServer устанавливает Wireguard на сервер, если его нет
func (wg *WireguardManager) SetupServer(server *models.Server) error {
	log.Printf("Начинаю настройку сервера %s:%d", server.IP, server.Port)

	// Устанавливаем соединение SSH с сервером с таймаутом
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	var client *ssh.Client
	var connectionErr error

	go func() {
		log.Printf("Попытка подключения к серверу %s:%d...", server.IP, server.Port)
		client, connectionErr = connectToServer(server)
		close(done)
	}()

	// Ожидаем завершения подключения или таймаута
	select {
	case <-done:
		if connectionErr != nil {
			log.Printf("Ошибка подключения к серверу %s:%d: %v", server.IP, server.Port, connectionErr)
			return fmt.Errorf("не удалось подключиться к серверу: %w", connectionErr)
		}
		log.Printf("Подключение к серверу %s:%d успешно установлено", server.IP, server.Port)
	case <-ctx.Done():
		log.Printf("Таймаут при подключении к серверу %s:%d", server.IP, server.Port)
		return fmt.Errorf("таймаут при подключении к серверу %s:%d", server.IP, server.Port)
	}

	defer client.Close()

	// Проверяем, установлен ли Wireguard
	log.Printf("Проверка наличия Wireguard на сервере...")
	installed, err := isWireguardInstalled(client)
	if err != nil {
		log.Printf("Ошибка при проверке установки Wireguard: %v", err)
		return fmt.Errorf("не удалось проверить наличие Wireguard: %w", err)
	}

	// Если не установлен, устанавливаем
	if !installed {
		log.Printf("Wireguard не установлен, начинаю установку...")
		err = installWireguard(client)
		if err != nil {
			log.Printf("Ошибка при установке Wireguard: %v", err)
			return fmt.Errorf("не удалось установить Wireguard: %w", err)
		}
		log.Printf("Wireguard успешно установлен")
	} else {
		log.Printf("Wireguard уже установлен на сервере")
	}

	// Проверяем/создаем базовую конфигурацию сервера
	log.Printf("Настройка конфигурации Wireguard...")
	err = setupServerConfig(client, server)
	if err != nil {
		log.Printf("Ошибка при настройке конфигурации сервера: %v", err)
		return fmt.Errorf("не удалось настроить конфигурацию сервера: %w", err)
	}

	log.Printf("Сервер %s:%d успешно настроен", server.IP, server.Port)
	return nil
}

// CreateClientConfig создает конфигурацию для нового клиента
func (wg *WireguardManager) CreateClientConfig(server *models.Server, clientName string) (string, error) {
	// Устанавливаем соединение SSH с сервером
	client, err := connectToServer(server)
	if err != nil {
		return "", fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()

	// Генерируем ключи клиента
	privateKey, publicKey, err := generateClientKeys(client)
	if err != nil {
		return "", fmt.Errorf("failed to generate client keys: %w", err)
	}

	// Получаем базовую информацию сервера
	serverInfo, err := getServerInfo(client)
	if err != nil {
		return "", fmt.Errorf("failed to get server info: %w", err)
	}

	// Получаем следующий свободный IP для клиента
	clientIP, err := getNextClientIP(client)
	if err != nil {
		return "", fmt.Errorf("failed to get next client IP: %w", err)
	}

	// Добавляем клиента на сервер
	err = addClientToServer(client, clientName, publicKey, clientIP)
	if err != nil {
		return "", fmt.Errorf("failed to add client to server: %w", err)
	}

	// Перезапускаем Wireguard
	err = restartWireguard(client)
	if err != nil {
		return "", fmt.Errorf("failed to restart Wireguard: %w", err)
	}

	// Создаем конфигурационный файл клиента
	configPath, err := createLocalClientConfig(wg.ConfigDir, clientName, privateKey, serverInfo, clientIP)
	if err != nil {
		return "", fmt.Errorf("failed to create client config: %w", err)
	}

	return configPath, nil
}

// RemoveClient удаляет клиента с сервера
func (wg *WireguardManager) RemoveClient(server *models.Server, clientName string) error {
	// Устанавливаем соединение SSH с сервером
	client, err := connectToServer(server)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()

	// Удаляем клиента с сервера
	err = removeClientFromServer(client, clientName)
	if err != nil {
		return fmt.Errorf("failed to remove client from server: %w", err)
	}

	// Перезапускаем Wireguard
	err = restartWireguard(client)
	if err != nil {
		return fmt.Errorf("failed to restart Wireguard: %w", err)
	}

	// Удаляем локальный файл конфигурации
	configPath := filepath.Join(wg.ConfigDir, clientName+".conf")
	if _, err := os.Stat(configPath); err == nil {
		err = os.Remove(configPath)
		if err != nil {
			return fmt.Errorf("failed to remove client config file: %w", err)
		}
	}

	return nil
}

// RevokeClientConfig отзывает конфигурацию клиента с сервера
func (wg *WireguardManager) RevokeClientConfig(server *models.Server, configFilePath string) error {
	// Извлекаем имя клиента из пути к файлу конфигурации
	if configFilePath == "" {
		return fmt.Errorf("empty config file path")
	}

	// Получаем только имя файла (без пути)
	_, fileName := filepath.Split(configFilePath)

	// Удаляем расширение .conf, чтобы получить имя клиента
	clientName := strings.TrimSuffix(fileName, ".conf")

	if clientName == "" {
		return fmt.Errorf("invalid client name extracted from config path: %s", configFilePath)
	}

	log.Printf("Отзыв конфигурации для клиента %s (файл: %s)", clientName, configFilePath)

	// Используем существующую функцию удаления клиента
	return wg.RemoveClient(server, clientName)
}

// BlockClient временно блокирует доступ клиента к VPN без удаления его конфигурации
func (wg *WireguardManager) BlockClient(server *models.Server, configFilePath string) error {
	// Извлекаем имя клиента из пути к файлу конфигурации
	if configFilePath == "" {
		return fmt.Errorf("empty config file path")
	}

	// Получаем только имя файла (без пути)
	_, fileName := filepath.Split(configFilePath)

	// Удаляем расширение .conf, чтобы получить имя клиента
	clientName := strings.TrimSuffix(fileName, ".conf")

	if clientName == "" {
		return fmt.Errorf("invalid client name extracted from config path: %s", configFilePath)
	}

	log.Printf("Блокировка доступа для клиента %s (файл: %s)", clientName, configFilePath)

	// Устанавливаем соединение SSH с сервером
	client, err := connectToServer(server)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()

	// Создаем временный файл с закомментированным клиентом
	cmd := fmt.Sprintf(`sed -i 's/^# %s$/#BLOCKED %s/g; s/^\[Peer\]/#[Peer]/g; s/^PublicKey/#PublicKey/g; s/^AllowedIPs/#AllowedIPs/g' /etc/wireguard/wg0.conf`, clientName, clientName)
	_, err = executeCommand(client, cmd)
	if err != nil {
		return fmt.Errorf("failed to block client: %w", err)
	}

	// Перезапускаем Wireguard
	err = restartWireguard(client)
	if err != nil {
		return fmt.Errorf("failed to restart Wireguard after blocking client: %w", err)
	}

	return nil
}

// UnblockClient восстанавливает доступ ранее заблокированного клиента к VPN
func (wg *WireguardManager) UnblockClient(server *models.Server, configFilePath string) error {
	// Извлекаем имя клиента из пути к файлу конфигурации
	if configFilePath == "" {
		return fmt.Errorf("empty config file path")
	}

	// Получаем только имя файла (без пути)
	_, fileName := filepath.Split(configFilePath)

	// Удаляем расширение .conf, чтобы получить имя клиента
	clientName := strings.TrimSuffix(fileName, ".conf")

	if clientName == "" {
		return fmt.Errorf("invalid client name extracted from config path: %s", configFilePath)
	}

	log.Printf("Разблокировка доступа для клиента %s (файл: %s)", clientName, configFilePath)

	// Устанавливаем соединение SSH с сервером
	client, err := connectToServer(server)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()

	// Создаем временный файл с разблокированным клиентом
	cmd := fmt.Sprintf(`sed -i 's/^#BLOCKED %s$/# %s/g; s/^#\[Peer\]/[Peer]/g; s/^#PublicKey/PublicKey/g; s/^#AllowedIPs/AllowedIPs/g' /etc/wireguard/wg0.conf`, clientName, clientName)
	_, err = executeCommand(client, cmd)
	if err != nil {
		return fmt.Errorf("failed to unblock client: %w", err)
	}

	// Перезапускаем Wireguard
	err = restartWireguard(client)
	if err != nil {
		return fmt.Errorf("failed to restart Wireguard after unblocking client: %w", err)
	}

	return nil
}

// IsClientBlocked проверяет, заблокирован ли клиент на сервере
func (wg *WireguardManager) IsClientBlocked(server *models.Server, configFilePath string) (bool, error) {
	// Извлекаем имя клиента из пути к файлу конфигурации
	if configFilePath == "" {
		return false, fmt.Errorf("empty config file path")
	}

	// Получаем только имя файла (без пути)
	_, fileName := filepath.Split(configFilePath)

	// Удаляем расширение .conf, чтобы получить имя клиента
	clientName := strings.TrimSuffix(fileName, ".conf")

	if clientName == "" {
		return false, fmt.Errorf("invalid client name extracted from config path: %s", configFilePath)
	}

	// Устанавливаем соединение SSH с сервером
	client, err := connectToServer(server)
	if err != nil {
		return false, fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()

	// Проверяем, есть ли заблокированный клиент в конфиге
	cmd := fmt.Sprintf(`grep -c "#BLOCKED %s" /etc/wireguard/wg0.conf || echo "0"`, clientName)
	output, err := executeCommand(client, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to check if client is blocked: %w", err)
	}

	// Если найдено хотя бы одно совпадение, клиент заблокирован
	count, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return false, fmt.Errorf("failed to parse grep result: %w", err)
	}

	return count > 0, nil
}

// Вспомогательные функции

// connectToServer устанавливает SSH соединение с сервером
func connectToServer(server *models.Server) (*ssh.Client, error) {
	log.Printf("Подключение к серверу %s:%d...", server.IP, server.Port)

	// Пингуем сервер перед подключением для проверки доступности
	timeout := 5 * time.Second
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", server.IP, server.Port), timeout)
	if err != nil {
		log.Printf("Сервер %s:%d недоступен: %v", server.IP, server.Port, err)
		return nil, fmt.Errorf("сервер недоступен: %w", err)
	}
	conn.Close()

	// Настройка SSH клиента
	config := &ssh.ClientConfig{
		User: server.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.SSHPassword),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// Формирование адреса
	addr := fmt.Sprintf("%s:%d", server.IP, server.Port)

	log.Printf("Выполняется SSH-подключение к серверу %s...", addr)

	// Соединение с сервером
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		log.Printf("Ошибка SSH-подключения к %s: %v", addr, err)
		return nil, fmt.Errorf("ошибка SSH-подключения к %s: %w", addr, err)
	}

	log.Printf("SSH-подключение к серверу %s успешно установлено", addr)

	// Проверка доступа к команде sudo
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		log.Printf("Не удалось создать SSH-сессию: %v", err)
		return nil, fmt.Errorf("не удалось создать SSH-сессию: %w", err)
	}
	defer session.Close()

	err = session.Run("sudo -n true")
	if err != nil {
		client.Close()
		log.Printf("У пользователя %s нет прав sudo без пароля: %v", server.SSHUser, err)
		return nil, fmt.Errorf("у пользователя %s нет прав sudo без пароля, необходимых для настройки сервера", server.SSHUser)
	}

	return client, nil
}

// executeCommand выполняет команду на сервере через SSH
func executeCommand(client *ssh.Client, command string) (string, error) {
	// Создаем сессию
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Буферы для вывода
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Выполняем команду
	err = session.Run(command)
	if err != nil {
		return "", fmt.Errorf("command execution failed: %s, error: %w", stderr.String(), err)
	}

	return stdout.String(), nil
}

// isWireguardInstalled проверяет, установлен ли Wireguard на сервере
func isWireguardInstalled(client *ssh.Client) (bool, error) {
	output, err := executeCommand(client, "which wg")
	if err != nil {
		// Команда может вернуть ошибку, если wireguard не установлен
		return false, nil
	}

	return strings.TrimSpace(output) != "", nil
}

// installWireguard устанавливает Wireguard на сервер
func installWireguard(client *ssh.Client) error {
	// Определяем тип ОС
	osType, err := executeCommand(client, "cat /etc/os-release | grep -E '^(ID|ID_LIKE)=' | head -1")
	if err != nil {
		return fmt.Errorf("failed to determine OS type: %w", err)
	}

	var installCmd string
	if strings.Contains(osType, "debian") || strings.Contains(osType, "ubuntu") {
		// Обновляем списки пакетов и устанавливаем WireGuard
		_, err = executeCommand(client, "apt update")
		if err != nil {
			return fmt.Errorf("failed to update package list: %w", err)
		}

		installCmd = "apt install -y wireguard wireguard-tools"
	} else if strings.Contains(osType, "fedora") || strings.Contains(osType, "centos") || strings.Contains(osType, "rhel") {
		// Устанавливаем epel-release если это RHEL или CentOS
		if strings.Contains(osType, "centos") || strings.Contains(osType, "rhel") {
			_, err = executeCommand(client, "yum install -y epel-release")
			if err != nil {
				// Игнорируем ошибку, если пакет уже установлен
				log.Printf("Warning: epel-release installation failed, continuing: %v", err)
			}
		}

		// Устанавливаем wireguard-tools
		installCmd = "yum install -y wireguard-tools"
	} else if strings.Contains(osType, "arch") {
		installCmd = "pacman -Sy --noconfirm wireguard-tools"
	} else if strings.Contains(osType, "alpine") {
		installCmd = "apk add --update wireguard-tools"
	} else {
		// Для неизвестных дистрибутивов попробуем универсальный подход
		log.Printf("Unknown OS type: %s, trying generic installation method", osType)

		// Пробуем apt (Debian, Ubuntu, и др.)
		_, err = executeCommand(client, "which apt && apt update && apt install -y wireguard wireguard-tools")
		if err == nil {
			return nil
		}

		// Пробуем yum (RHEL, CentOS, Fedora)
		_, err = executeCommand(client, "which yum && yum install -y wireguard-tools")
		if err == nil {
			return nil
		}

		// Пробуем pacman (Arch Linux)
		_, err = executeCommand(client, "which pacman && pacman -Sy --noconfirm wireguard-tools")
		if err == nil {
			return nil
		}

		// Пробуем apk (Alpine Linux)
		_, err = executeCommand(client, "which apk && apk add --update wireguard-tools")
		if err == nil {
			return nil
		}

		return fmt.Errorf("unsupported OS for automatic installation, please install wireguard manually")
	}

	// Выполняем команду установки
	_, err = executeCommand(client, installCmd)
	if err != nil {
		return fmt.Errorf("failed to install Wireguard: %w", err)
	}

	// Проверяем, что wireguard успешно установлен
	_, err = executeCommand(client, "which wg")
	if err != nil {
		return fmt.Errorf("wireguard installation failed, wg command not found: %w", err)
	}

	log.Println("Wireguard successfully installed")
	return nil
}

// setupServerConfig настраивает базовую конфигурацию Wireguard на сервере
func setupServerConfig(client *ssh.Client, server *models.Server) error {
	// Проверяем наличие каталога для Wireguard
	_, err := executeCommand(client, "mkdir -p /etc/wireguard")
	if err != nil {
		return fmt.Errorf("failed to create wireguard directory: %w", err)
	}

	// Проверяем наличие файла конфигурации
	output, err := executeCommand(client, "test -f /etc/wireguard/wg0.conf && echo 'exists'")
	if err == nil && strings.TrimSpace(output) == "exists" {
		// Проверяем наличие ключей
		output, err = executeCommand(client, "test -f /etc/wireguard/server_public.key && echo 'exists'")
		if err == nil && strings.TrimSpace(output) == "exists" {
			// Конфигурация и ключи уже существуют
			return nil
		}
	}

	// Генерируем ключи сервера
	_, err = executeCommand(client, "wg genkey | tee /etc/wireguard/server_private.key | wg pubkey > /etc/wireguard/server_public.key")
	if err != nil {
		return fmt.Errorf("failed to generate server keys: %w", err)
	}

	// Проверяем, что ключи были созданы
	output, err = executeCommand(client, "test -f /etc/wireguard/server_private.key && test -f /etc/wireguard/server_public.key && echo 'success'")
	if err != nil || strings.TrimSpace(output) != "success" {
		return fmt.Errorf("failed to verify server keys were created")
	}

	// Получаем приватный ключ
	privateKey, err := executeCommand(client, "cat /etc/wireguard/server_private.key")
	if err != nil {
		return fmt.Errorf("failed to read server private key: %w", err)
	}
	privateKey = strings.TrimSpace(privateKey)

	if privateKey == "" {
		return fmt.Errorf("server private key is empty")
	}

	// Определяем основной сетевой интерфейс
	netInterface, err := executeCommand(client, "ip -o -4 route show to default | awk '{print $5}' | head -1")
	if err != nil {
		// Если не удалось определить, используем eth0 по умолчанию
		netInterface = "eth0"
	} else {
		netInterface = strings.TrimSpace(netInterface)
		if netInterface == "" {
			netInterface = "eth0"
		}
	}

	// Создаем базовый конфигурационный файл
	serverConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.0.0.1/24
ListenPort = 51820
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o %s -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o %s -j MASQUERADE
`, privateKey, netInterface, netInterface)

	// Записываем конфигурацию сервера
	tempFile := "/tmp/wg0.conf"
	err = writeFileToServer(client, tempFile, serverConfig)
	if err != nil {
		return fmt.Errorf("failed to write server config: %w", err)
	}

	// Перемещаем файл в нужное место
	_, err = executeCommand(client, fmt.Sprintf("mv %s /etc/wireguard/wg0.conf", tempFile))
	if err != nil {
		return fmt.Errorf("failed to move server config: %w", err)
	}

	// Устанавливаем правильные разрешения
	_, err = executeCommand(client, "chmod 600 /etc/wireguard/wg0.conf /etc/wireguard/server_private.key /etc/wireguard/server_public.key")
	if err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	// Включаем IP forwarding
	_, err = executeCommand(client, "echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-wireguard.conf && sysctl -p /etc/sysctl.d/99-wireguard.conf")
	if err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Запускаем Wireguard
	_, err = executeCommand(client, "systemctl enable wg-quick@wg0 && systemctl start wg-quick@wg0")
	if err != nil {
		return fmt.Errorf("failed to start Wireguard: %w", err)
	}

	// Проверяем, что интерфейс wg0 поднялся
	output, err = executeCommand(client, "ip a show wg0")
	if err != nil {
		return fmt.Errorf("failed to verify wireguard interface: %w", err)
	}

	return nil
}

// writeFileToServer записывает файл на сервер
func writeFileToServer(client *ssh.Client, path, content string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	cmd := fmt.Sprintf("cat > %s", path)
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	err = session.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	_, err = stdin.Write([]byte(content))
	if err != nil {
		return fmt.Errorf("failed to write to stdin: %w", err)
	}

	err = stdin.Close()
	if err != nil {
		return fmt.Errorf("failed to close stdin: %w", err)
	}

	err = session.Wait()
	if err != nil {
		return fmt.Errorf("command execution failed: %s, error: %w", stderr.String(), err)
	}

	return nil
}

// generateClientKeys генерирует ключи для клиента
func generateClientKeys(client *ssh.Client) (string, string, error) {
	// Генерируем приватный ключ
	output, err := executeCommand(client, "wg genkey")
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}
	privateKey := strings.TrimSpace(output)

	// Генерируем публичный ключ
	cmd := fmt.Sprintf("echo '%s' | wg pubkey", privateKey)
	output, err = executeCommand(client, cmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate public key: %w", err)
	}
	publicKey := strings.TrimSpace(output)

	return privateKey, publicKey, nil
}

// getServerInfo получает информацию о сервере
func getServerInfo(client *ssh.Client) (map[string]string, error) {
	info := make(map[string]string)

	// Проверяем наличие ключей и создаем их при необходимости
	output, err := executeCommand(client, "test -f /etc/wireguard/server_public.key && echo 'exists'")
	if err != nil || strings.TrimSpace(output) != "exists" {
		// Ключи не существуют, генерируем их
		log.Println("Server keys not found, generating new ones...")

		// Убедимся, что каталог существует
		_, err = executeCommand(client, "mkdir -p /etc/wireguard")
		if err != nil {
			return nil, fmt.Errorf("failed to create wireguard directory: %w", err)
		}

		// Генерируем ключи сервера
		_, err = executeCommand(client, "wg genkey | tee /etc/wireguard/server_private.key | wg pubkey > /etc/wireguard/server_public.key")
		if err != nil {
			return nil, fmt.Errorf("failed to generate server keys: %w", err)
		}

		// Устанавливаем правильные разрешения
		_, err = executeCommand(client, "chmod 600 /etc/wireguard/server_private.key /etc/wireguard/server_public.key")
		if err != nil {
			return nil, fmt.Errorf("failed to set permissions for server keys: %w", err)
		}
	}

	// Теперь получаем публичный ключ сервера
	output, err = executeCommand(client, "cat /etc/wireguard/server_public.key")
	if err != nil {
		return nil, fmt.Errorf("failed to get server public key: %w", err)
	}
	serverPublicKey := strings.TrimSpace(output)
	if serverPublicKey == "" {
		return nil, fmt.Errorf("server public key is empty")
	}
	info["ServerPublicKey"] = serverPublicKey

	// Получаем внешний IP сервера
	output, err = executeCommand(client, "curl -s ifconfig.me || curl -s api.ipify.org || curl -s icanhazip.com")
	if err != nil {
		// Если не удалось получить IP через curl, пробуем другой метод
		output, err = executeCommand(client, "hostname -I | awk '{print $1}'")
		if err != nil {
			return nil, fmt.Errorf("failed to get server IP: %w", err)
		}
	}
	info["ServerPublicIP"] = strings.TrimSpace(output)

	// Получаем порт сервера
	output, err = executeCommand(client, "grep ListenPort /etc/wireguard/wg0.conf | cut -d'=' -f2")
	if err != nil {
		info["ServerPort"] = "51820" // Порт по умолчанию, если не удалось найти в файле
	} else {
		port := strings.TrimSpace(output)
		if port == "" {
			info["ServerPort"] = "51820" // Порт по умолчанию, если строка пустая
		} else {
			info["ServerPort"] = port
		}
	}

	return info, nil
}

// getNextClientIP получает следующий свободный IP для клиента
func getNextClientIP(client *ssh.Client) (string, error) {
	// Получаем список существующих пиров
	output, err := executeCommand(client, "grep AllowedIPs /etc/wireguard/wg0.conf")
	if err != nil {
		// Если ошибка, возможно нет пиров
		return "10.0.0.2/32", nil
	}

	lines := strings.Split(output, "\n")
	maxIP := 1 // Сервер имеет 10.0.0.1

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Извлекаем IP
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			continue
		}

		ipWithCIDR := strings.TrimSpace(parts[1])
		ipOnly := strings.Split(ipWithCIDR, "/")[0]
		ipParts := strings.Split(ipOnly, ".")
		if len(ipParts) < 4 {
			continue
		}

		lastPart, err := strconv.Atoi(ipParts[3])
		if err != nil {
			continue
		}

		if lastPart > maxIP {
			maxIP = lastPart
		}
	}

	// Следующий IP
	return fmt.Sprintf("10.0.0.%d/32", maxIP+1), nil
}

// addClientToServer добавляет клиента на сервер
func addClientToServer(client *ssh.Client, clientName, publicKey, clientIP string) error {
	// Создаем конфигурацию клиента
	clientConfig := fmt.Sprintf(`
# %s
[Peer]
PublicKey = %s
AllowedIPs = %s
`, clientName, publicKey, clientIP)

	// Добавляем конфигурацию в файл сервера
	_, err := executeCommand(client, fmt.Sprintf("echo '%s' >> /etc/wireguard/wg0.conf", clientConfig))
	if err != nil {
		return fmt.Errorf("failed to add client config to server: %w", err)
	}

	return nil
}

// createLocalClientConfig создает локальный файл конфигурации клиента
func createLocalClientConfig(configDir, clientName, privateKey string, serverInfo map[string]string, clientIP string) (string, error) {
	clientIP = strings.Split(clientIP, "/")[0] // Удаляем CIDR

	// Создаем содержимое файла конфигурации
	configContent := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32
DNS = 8.8.8.8, 1.1.1.1

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0
Endpoint = %s:%s
PersistentKeepalive = 25
`, privateKey, clientIP, serverInfo["ServerPublicKey"], serverInfo["ServerPublicIP"], serverInfo["ServerPort"])

	// Создаем полный путь к файлу
	configPath := filepath.Join(configDir, clientName+".conf")

	// Записываем файл
	err := ioutil.WriteFile(configPath, []byte(configContent), 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write client config file: %w", err)
	}

	return configPath, nil
}

// removeClientFromServer удаляет клиента с сервера
func removeClientFromServer(client *ssh.Client, clientName string) error {
	// Создаем временный файл без клиента
	cmd := fmt.Sprintf("grep -v '# %s' /etc/wireguard/wg0.conf | grep -v -A2 '# %s' > /tmp/wg0.conf.tmp && mv /tmp/wg0.conf.tmp /etc/wireguard/wg0.conf", clientName, clientName)
	_, err := executeCommand(client, cmd)
	if err != nil {
		return fmt.Errorf("failed to remove client from config: %w", err)
	}

	return nil
}

// restartWireguard перезапускает сервис Wireguard
func restartWireguard(client *ssh.Client) error {
	_, err := executeCommand(client, "systemctl restart wg-quick@wg0")
	if err != nil {
		return fmt.Errorf("failed to restart Wireguard: %w", err)
	}

	return nil
}
