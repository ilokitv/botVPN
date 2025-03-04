#!/bin/bash

# Проверяем наличие необходимых зависимостей
echo "Проверка зависимостей..."

# Проверка Go
if ! command -v go &> /dev/null; then
    echo "ОШИБКА: Go не установлен. Установите Go версии 1.21 или выше."
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "Go версия: $GO_VERSION"

# Проверка PostgreSQL
if ! command -v psql &> /dev/null; then
    echo "ОШИБКА: PostgreSQL не установлен. Установите PostgreSQL."
    exit 1
fi

PSQL_VERSION=$(psql --version | awk '{print $3}')
echo "PostgreSQL версия: $PSQL_VERSION"

# Проверка зависимостей Go
echo "Проверка зависимостей Go..."
go mod download
if [ $? -ne 0 ]; then
    echo "ОШИБКА: Не удалось загрузить зависимости Go."
    exit 1
fi

echo "Все зависимости проверены успешно!"
exit 0 