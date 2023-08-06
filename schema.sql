create table request_logs (
    request  jsonb       not null,
    response jsonb       not null,
    ts       timestamptz not null,
    primary key (id)
)
