Top %top_n% update (%convert%) — new entrants detected at %timestamp_utc%:

%EACH new_coins%- #%rank% %name% (%symbol%)
  Market cap: %market_cap|n/a% %market_cap_currency|%%
%END_EACH%

%IF exited_coins%Exited (notify_exits=true):
%EACH exited_coins%- #%rank% %name% (%symbol%)
%END_EACH%
%END_IF%
