-- card word schema. Every statement is idempotent so a fresh database and an
-- existing one converge on the same shape.

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'owner',
    totp_secret   TEXT    NOT NULL DEFAULT '',
    totp_enabled  INTEGER NOT NULL DEFAULT 0,
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    updated_at    INTEGER NOT NULL DEFAULT 0
);

-- Pending second factor challenges. The token is stored hashed and expires
-- quickly; attempts are capped so a stolen challenge cannot be brute forced.
CREATE TABLE IF NOT EXISTS login_challenges (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_challenges_expires ON login_challenges(expires_at);

-- One time recovery codes for lost authenticators.
CREATE TABLE IF NOT EXISTS recovery_codes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT    NOT NULL,
    used_at   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_recovery_user ON recovery_codes(user_id);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT    NOT NULL UNIQUE,
    csrf_token   TEXT    NOT NULL,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    slug       TEXT    NOT NULL UNIQUE,
    sort       INTEGER NOT NULL DEFAULT 0,
    visible    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS products (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
    name        TEXT    NOT NULL,
    slug        TEXT    NOT NULL UNIQUE,
    summary     TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    price_cents INTEGER NOT NULL CHECK (price_cents >= 0),
    currency    TEXT    NOT NULL DEFAULT 'CNY',
    status      TEXT    NOT NULL DEFAULT 'active',
    min_qty     INTEGER NOT NULL DEFAULT 1,
    max_qty     INTEGER NOT NULL DEFAULT 10,
    sort        INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_products_status ON products(status, sort);

CREATE TABLE IF NOT EXISTS orders (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    order_no         TEXT    NOT NULL UNIQUE,
    product_id       INTEGER NOT NULL,
    product_name     TEXT    NOT NULL,
    quantity         INTEGER NOT NULL CHECK (quantity > 0),
    unit_price_cents INTEGER NOT NULL CHECK (unit_price_cents >= 0),
    subtotal_cents   INTEGER NOT NULL CHECK (subtotal_cents >= 0),
    discount_cents   INTEGER NOT NULL DEFAULT 0 CHECK (discount_cents >= 0),
    total_cents      INTEGER NOT NULL CHECK (total_cents >= 0),
    currency         TEXT    NOT NULL DEFAULT 'CNY',
    email            TEXT    NOT NULL DEFAULT '',
    contact          TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'pending',
    payment_driver   TEXT    NOT NULL DEFAULT '',
    payment_txn      TEXT    NOT NULL DEFAULT '',
    coupon_code      TEXT    NOT NULL DEFAULT '',
    query_token_hash TEXT    NOT NULL,
    idempotency_key  TEXT    NOT NULL DEFAULT '',
    client_ip        TEXT    NOT NULL DEFAULT '',
    note             TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    paid_at          INTEGER,
    delivered_at     INTEGER,
    closed_at        INTEGER
);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_orders_email ON orders(email);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_idem ON orders(idempotency_key);

CREATE TABLE IF NOT EXISTS card_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    secret     TEXT    NOT NULL,
    masked     TEXT    NOT NULL,
    label      TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'available',
    order_id   INTEGER,
    sold_at    INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cards_stock ON card_keys(product_id, status);
CREATE INDEX IF NOT EXISTS idx_cards_order ON card_keys(order_id);

CREATE TABLE IF NOT EXISTS order_cards (
    order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    card_id  INTEGER NOT NULL REFERENCES card_keys(id) ON DELETE CASCADE,
    PRIMARY KEY (order_id, card_id)
);

CREATE TABLE IF NOT EXISTS payments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id     INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    driver       TEXT    NOT NULL,
    txn_id       TEXT    NOT NULL DEFAULT '',
    amount_cents INTEGER NOT NULL DEFAULT 0,
    status       TEXT    NOT NULL,
    raw          TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments(order_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_txn ON payments(driver, txn_id) WHERE txn_id <> '';

CREATE TABLE IF NOT EXISTS coupons (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    code            TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    kind            TEXT    NOT NULL,
    value           INTEGER NOT NULL,
    max_uses        INTEGER NOT NULL DEFAULT 0,
    used_count      INTEGER NOT NULL DEFAULT 0,
    min_total_cents INTEGER NOT NULL DEFAULT 0,
    product_id      INTEGER REFERENCES products(id) ON DELETE CASCADE,
    active          INTEGER NOT NULL DEFAULT 1,
    expires_at      INTEGER,
    created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    prefix       TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    scopes       TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER,
    expires_at   INTEGER,
    allowed_ips  TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS plugins (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    uid          TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL,
    version      TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    manifest     TEXT    NOT NULL,
    dir          TEXT    NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 0,
    sha256       TEXT    NOT NULL DEFAULT '',
    signature    TEXT    NOT NULL DEFAULT '',
    publisher    TEXT    NOT NULL DEFAULT '',
    permissions  TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT 'upload',
    error        TEXT    NOT NULL DEFAULT '',
    installed_at INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_settings (
    plugin_uid TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (plugin_uid, key)
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor      TEXT    NOT NULL,
    action     TEXT    NOT NULL,
    target     TEXT    NOT NULL DEFAULT '',
    ip         TEXT    NOT NULL DEFAULT '',
    detail     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
