-- +goose Up
-- Generic queue/staff permission points (ADR-0009 / design/10).
INSERT INTO permissions (code, name) VALUES
  ('queue.read', '查看队列与服务人员'),
  ('queue.write', '维护队列与服务人员')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('admin','manager') AND p.code IN ('queue.read','queue.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'staff' AND p.code = 'queue.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions WHERE permission_id IN (SELECT id FROM permissions WHERE code IN ('queue.read','queue.write'));
DELETE FROM permissions WHERE code IN ('queue.read','queue.write');
