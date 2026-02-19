You are writing a Telegram post for %project_name%.
Timestamp: %timestamp_utc%
Top-N universe: %top_n% (%convert%)

New entrants:
%EACH new_coins%- #%rank% %name% (%symbol%) id=%id% market_cap=%market_cap|n/a% %market_cap_currency|%%
%END_EACH%

%IF exited_coins%Exited (optional):
%EACH exited_coins%- #%rank% %name% (%symbol%)
%END_EACH%
%END_IF%

Recent posts (most recent first):
%EACH recent_posts%- [%created_at_utc%]
Text: %text%
Mentioned coins:
%EACH mentioned_coins%  - #%rank% %name% (%symbol%) market_cap=%market_cap|n/a% %market_cap_currency|%%
%END_EACH%

%END_EACH%

Write one concise Telegram-ready post in plain text (no markdown), with a short hook and bullet-like lines for each new coin.
