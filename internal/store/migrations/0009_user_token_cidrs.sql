-- Source networks a personal access token may be used from on the public dashboard host (ADR-0041). Empty: the
-- token works only on the loopback admin listener, as before.
ALTER TABLE user_tokens ADD COLUMN cidrs TEXT NOT NULL DEFAULT '[]';
