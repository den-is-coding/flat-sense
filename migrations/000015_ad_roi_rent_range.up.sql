-- Диапазон ожидаемой аренды p25–p75 по сценариям (issue #108, карточка
-- объекта карты: «Ожидаемая цена сдачи» + вилка). NULL — сценарий неприменим.
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS rent_p25_unfurnished DOUBLE PRECISION;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS rent_p75_unfurnished DOUBLE PRECISION;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS rent_p25_furnished   DOUBLE PRECISION;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS rent_p75_furnished   DOUBLE PRECISION;
