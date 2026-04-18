CREATE TABLE IF NOT EXISTS api_token_folder_permissions (
    id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    token_id      BIGINT       NOT NULL,
    folder_id     BIGINT       NULL,
    permission    VARCHAR(16)  NOT NULL,
    -- Generated column coalesces NULL folder_id to 0 so the unique index
    -- treats root (NULL) as a distinct slot rather than duplicatable.
    folder_id_key BIGINT       AS (COALESCE(folder_id, 0)) VIRTUAL,
    created_at    DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_api_token_fp_token  FOREIGN KEY (token_id)  REFERENCES api_tokens(id) ON DELETE CASCADE,
    CONSTRAINT fk_api_token_fp_folder FOREIGN KEY (folder_id) REFERENCES folders(id)    ON DELETE CASCADE,
    CONSTRAINT chk_api_token_fp_permission CHECK (permission IN ('read', 'write')),
    UNIQUE KEY uq_api_token_fp_folder (token_id, folder_id_key),
    INDEX idx_api_token_fp_token (token_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
