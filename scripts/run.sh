#!/bin/bash

# Проверка обновлений и сборка
echo "Собираем проект..."
go build -o bin/vpnbot cmd/bot/main.go

# Запуск бота
echo "Запускаем бота..."
./bin/vpnbot -config config.yaml 