DROP INDEX IF EXISTS idx_avito_listings_first_seen;
DROP INDEX IF EXISTS idx_avito_listings_complex;
DROP INDEX IF EXISTS idx_avito_listings_city;
ALTER TABLE avito_listings DROP COLUMN IF EXISTS residential_complex;
