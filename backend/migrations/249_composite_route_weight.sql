-- composite 跨平台负载均衡：路由权重。
--
-- 0（默认）= 不参与分流，只作为 failover 备援（现有行为完全不变）。
-- >0        = 参与加权分流；同一 public model 下所有 weight>0 的路由按比例
--             分配会话。
--
-- 分流以「会话」为单位固定：同一段对话整个生命周期绑定到同一平台，避免
-- prompt cache 与 previous_response_id 在平台间跳动而失效。平台失败时仍按
-- priority 顺序 failover。
ALTER TABLE composite_model_routes
    ADD COLUMN IF NOT EXISTS weight INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN composite_model_routes.weight IS
    '跨平台分流权重；0 表示仅作 failover 备援，不参与分流';
