-- ============================================================
-- Symbiont — Migration 003 rollback
-- ============================================================

DROP TABLE IF EXISTS blueprint_instances;
DROP TABLE IF EXISTS blueprints;
DROP TABLE IF EXISTS marketplace_plugins;
DROP TYPE  IF EXISTS blueprint_category;
