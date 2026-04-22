BEGIN;

DROP INDEX IF EXISTS reporting.widgets_dashboard_id_kind_idx;
DROP TABLE IF EXISTS reporting.widgets;

DROP INDEX IF EXISTS reporting.dashboards_title_uidx;
DROP TABLE IF EXISTS reporting.dashboards;

COMMIT;
