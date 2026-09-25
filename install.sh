#!/usr/bin/env bash
# WGPanel Installer (iransinfo edition)
set -euo pipefail

readonly INSTALL_DIR="/opt/wgpanel"
readonly REPO_RAW_BASE="https://raw.githubusercontent.com/iransinfo/wgpanel/main"

mkdir -p "${INSTALL_DIR}"
echo "==> Starting WGPanel Installation..."

# 1. نصب داکر در صورت عدم وجود
if ! command -v docker &>/dev/null; then
    echo "==> Installing Docker..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable --now docker
fi

# 2. دریافت فایل‌های پیکربندی آماده از پوشه deploy
cd "${INSTALL_DIR}"
curl -fsSL "${REPO_RAW_BASE}/deploy/docker-compose.yml" -o docker-compose.yml
curl -fsSL "${REPO_RAW_BASE}/deploy/Caddyfile" -o Caddyfile 2>/dev/null || true

# 3. دریافت اطلاعات دامنه و تنظیم سکرت‌ها
if [[ ! -f "${INSTALL_DIR}/.env" ]]; then
    read -rp "Enter Panel Domain (e.g. vpn.example.com): " DOMAIN
    read -rp "Enter Admin Email (for SSL): " ACME_EMAIL
    read -rp "Enter Admin Username [admin]: " ADMIN_USER
    ADMIN_USER=${ADMIN_USER:-admin}
    ADMIN_PASS=$(openssl rand -base64 12 | tr -dc 'a-zA-Z0-9' | head -c 12)
    JWT_SECRET=$(openssl rand -hex 32)
    API_KEY=$(openssl rand -hex 32)
    DB_PASSWORD=$(openssl rand -hex 16)

    cat << ENVEOF > "${INSTALL_DIR}/.env"
DOMAIN=${DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
WEB_PORT=443
HTTP_PORT=80
JWT_SECRET=${JWT_SECRET}
ADMIN_USER=${ADMIN_USER}
ADMIN_PASSWORD=${ADMIN_PASS}
NODE_API_KEY=${API_KEY}
DB_PASSWORD=${DB_PASSWORD}
ENVEOF
fi

# 4. دریافت ایمیج‌های آماده بدون کامپایل (سریع)
echo "==> Pulling pre-built images from GHCR..."
docker compose pull

# 5. بالا آوردن سرویس‌ها
echo "==> Starting containers..."
docker compose up -d --force-recreate

echo "=========================================="
echo " WGPanel Installed Successfully!"
echo " Domain: https://${DOMAIN:-your-domain}"
echo "=========================================="
