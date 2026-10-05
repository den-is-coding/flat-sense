ALTER TABLE ad_furnishing
    DROP COLUMN IF EXISTS furnished_photo,
    DROP COLUMN IF EXISTS furnished_photo_confidence,
    DROP COLUMN IF EXISTS furnished_photo_at;
