-- 0010 — variable references (down).
--
-- The forward migration is additive (CREATE TABLE / CREATE INDEX with IF
-- NOT EXISTS). The reverse path drops only the table and indexes the
-- forward migration introduced. No other 0001-0009 objects are touched.

DROP INDEX IF EXISTS idx_service_variables_kind;
DROP INDEX IF EXISTS idx_service_variables_scope;
DROP TABLE IF EXISTS service_variables;
