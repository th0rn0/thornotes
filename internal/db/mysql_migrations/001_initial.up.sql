CREATE TABLE IF NOT EXISTS users (
    id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(255) NOT NULL,
    password_hash TEXT         NOT NULL,
    created_at    DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    UNIQUE KEY uq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS sessions (
    token      VARCHAR(255) NOT NULL PRIMARY KEY,
    user_id    BIGINT       NOT NULL,
    expires_at DATETIME     NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    INDEX idx_sessions_user_id (user_id),
    INDEX idx_sessions_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS folders (
    id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id       BIGINT       NOT NULL,
    parent_id     BIGINT       NULL,
    name          VARCHAR(255) NOT NULL,
    disk_path     VARCHAR(4096) NOT NULL,
    -- Generated column coalesces NULL parent_id to 0 for the unique index.
    -- MySQL UNIQUE indexes treat NULL as distinct, so without this trick
    -- multiple root folders with the same name would be allowed.
    parent_id_key BIGINT       AS (COALESCE(parent_id, 0)) VIRTUAL,
    created_at    DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_folders_user   FOREIGN KEY (user_id)   REFERENCES users(id)   ON DELETE CASCADE,
    CONSTRAINT fk_folders_parent FOREIGN KEY (parent_id) REFERENCES folders(id) ON DELETE CASCADE,
    UNIQUE KEY uq_disk_path (disk_path(768)),
    UNIQUE KEY uq_folders_name (user_id, parent_id_key, name),
    INDEX idx_folders_user_id (user_id),
    INDEX idx_folders_parent_id (parent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS notes (
    id            BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id       BIGINT        NOT NULL,
    folder_id     BIGINT        NULL,
    title         VARCHAR(1024) NOT NULL,
    slug          VARCHAR(1024) NOT NULL,
    disk_path     VARCHAR(4096) NOT NULL,
    content       LONGTEXT      NOT NULL DEFAULT '',
    content_hash  VARCHAR(255)  NOT NULL DEFAULT '',
    tags          JSON          NOT NULL,
    share_token   VARCHAR(255)  NULL,
    fts_synced_at DATETIME      NULL,
    created_at    DATETIME      NOT NULL DEFAULT (UTC_TIMESTAMP()),
    updated_at    DATETIME      NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_notes_user   FOREIGN KEY (user_id)   REFERENCES users(id)   ON DELETE CASCADE,
    CONSTRAINT fk_notes_folder FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE SET NULL,
    UNIQUE KEY uq_notes_share_token (share_token),
    UNIQUE KEY uq_notes_disk_path (disk_path(768)),
    INDEX idx_notes_user_id (user_id),
    INDEX idx_notes_folder_id (folder_id),
    FULLTEXT INDEX idx_notes_fts (title, content)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
