#!/bin/bash

# Запускаем скрипт инициализации базы данных
echo "Инициализация базы данных..."
psql -U postgres -f scripts/init_db.sql

echo "База данных успешно инициализирована!" 