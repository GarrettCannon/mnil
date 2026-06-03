#!/usr/bin/env bash
# Synthetic log source used by docs/demo.tape to produce a varied stream
# of log lines for the README demo gif.
while true; do
    ts=$(date +%H:%M:%S)
    case $((RANDOM % 9)) in
        0) echo "$ts [INFO] request handled in ${RANDOM}ms" ;;
        1) echo "$ts [WARN] cache miss for key=user:$RANDOM" ;;
        2) echo "$ts [ERROR] connection refused to db:5432" ;;
        3) echo "$ts [INFO] GET /api/users 200 from 192.168.1.42" ;;
        4) echo "$ts [DEBUG] compiled $RANDOM modules in 1.8s" ;;
        5) echo "$ts [INFO] webhook delivered to https://example.com/hook" ;;
        6) echo "$ts [INFO] GET /api/v2/users?include=profile,settings,permissions,roles,teams,subscriptions&filter[status]=active&sort=-created_at&page[size]=100&page[number]=${RANDOM} 200 in 187ms from 192.168.1.42" ;;
        7) echo "$ts [ERROR] failed to deserialize payload {\"id\":${RANDOM},\"event\":\"user.updated\",\"data\":{\"email\":\"jane@example.com\",\"roles\":[\"admin\",\"editor\",\"viewer\"],\"metadata\":{\"source\":\"web\",\"campaign\":\"summer-launch-2026\",\"trace_id\":\"abc-${RANDOM}\"}}}: unexpected field \"campaign\" at line 1 col 142" ;;
        8) echo "$ts [WARN] slow query (${RANDOM}ms): SELECT u.id, u.email, p.name, p.bio, s.tier FROM users u JOIN profiles p ON p.user_id = u.id LEFT JOIN subscriptions s ON s.user_id = u.id WHERE u.last_seen_at > NOW() - INTERVAL '30 days' ORDER BY u.created_at DESC LIMIT 500" ;;
    esac
    sleep 0.15
done
