-- Кэш фото-детекции меблировки (issue #110): результат фото-фолбэка,
-- чтобы не пересматривать фото повторно. Текстовый признак — в
-- furnishing/confident; фото — слабый сигнал, применяется только когда
-- текст не дал уверенного ответа.
ALTER TABLE ad_furnishing
    ADD COLUMN IF NOT EXISTS furnished_photo TEXT,
    ADD COLUMN IF NOT EXISTS furnished_photo_confidence REAL,
    ADD COLUMN IF NOT EXISTS furnished_photo_at TIMESTAMPTZ;
