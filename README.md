# Stuffpot

Stuffpot is a HTTP proxy intended to the utilized as a honeypot. It logs requests made to the proxy, and attempts to
MITM `https` tunnels.

## Output

All requests sent through the proxy are recorded to an sqlite database named `log.db`. The database can then be used
analyse and detect malicious traffic. 


