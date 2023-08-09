create table request_logs (
    request  jsonb       not null,
    response jsonb       not null,
    ts       timestamptz not null
);
create index request_logs_ts_idx on request_logs (ts);
