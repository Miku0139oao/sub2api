-- 允許同一 public_model 存在多條路由。
--
-- 172 建了 idx_composite_model_routes_unique_active，把
-- (group_id, endpoint, match_type, public_model) 綁成唯一鍵，因此一個公開模型
-- 只能有一條路由 —— 跨平台 failover 與負載均衡都無從設定。
--
-- 這個索引在建表當時是合理的（那時每個模型只對應單一平台）。現在同一模型的
-- 多條路由是正常設定：依 priority 形成候選鏈，依 weight 分配流量。
--
-- 誤設定（同 group + endpoint + match_type + public_model + target_platform
-- 重複）由應用層擋下，不需要資料庫唯一索引。
DROP INDEX IF EXISTS idx_composite_model_routes_unique_active;

-- 候選鏈的查詢走 (group_id, public_model) 過濾後再排序，補一個對應索引。
CREATE INDEX IF NOT EXISTS idx_composite_model_routes_group_model
    ON composite_model_routes (group_id, public_model, priority, id)
    WHERE deleted_at IS NULL;
