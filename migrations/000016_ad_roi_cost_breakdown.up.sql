-- Раскладка полной стоимости сделки (issue #108, карточка объекта:
-- «сколько на меблировку, сколько на комиссии, сколько риэлтору»).
-- Считает analyzer-service при backfill; админ/карта только читают.
-- realtor_fee — комиссия риэлтора; deal_costs_other — титульное
-- страхование и оформление; furnishing_cost — надбавка на меблировку
-- (0, если не применялась). NULL — расчёт старой версии кэша.
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS furnishing_cost  BIGINT;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS realtor_fee      BIGINT;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS deal_costs_other BIGINT;
ALTER TABLE ad_roi_results ADD COLUMN IF NOT EXISTS deal_costs_total BIGINT;
