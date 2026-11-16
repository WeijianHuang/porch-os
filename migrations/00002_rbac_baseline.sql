-- +goose Up
-- Generic RBAC baseline. No business/industry data is seeded here.
INSERT INTO permissions (code, name) VALUES
  ('shop.read', '查看门店'),
  ('shop.write', '维护门店'),
  ('catalog.read', '查看目录'),
  ('catalog.write', '维护目录'),
  ('member.read', '查看会员'),
  ('member.write', '维护会员'),
  ('report.cross_shop', '跨店汇总')
ON CONFLICT (code) DO NOTHING;

INSERT INTO roles (code, name) VALUES
  ('admin', '管理员'),
  ('manager', '店长'),
  ('staff', '店员')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.code = 'admin'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'manager' AND p.code <> 'report.cross_shop'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'staff' AND p.code IN ('catalog.read','member.read')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions;
DELETE FROM user_roles;
DELETE FROM roles;
DELETE FROM permissions;
