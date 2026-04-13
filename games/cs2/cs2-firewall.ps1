# cs2-firewall.ps1 — Manage Windows Firewall rules for CS2 LAN servers
# Run as Administrator in PowerShell
#
# Usage:
#   .\cs2-firewall.ps1 enable                # allow LAN connections (game ports only)
#   .\cs2-firewall.ps1 enable -WebPort 8080  # also open the web panel port
#   .\cs2-firewall.ps1 disable               # remove all firewall rules

param(
    [Parameter(Mandatory=$true, Position=0)]
    [ValidateSet("enable", "disable")]
    [string]$Action,

    [Parameter(Mandatory=$false)]
    [int]$WebPort = 0
)

switch ($Action) {
    "enable" {
        Remove-NetFirewallRule -DisplayName "CS2 LAN Servers" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN Servers TCP" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN CSTV" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN CSTV TCP" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN Panel" -ErrorAction SilentlyContinue
        New-NetFirewallRule -DisplayName "CS2 LAN Servers" -Direction Inbound -Protocol UDP -LocalPort 27015-27030 -Action Allow | Out-Null
        New-NetFirewallRule -DisplayName "CS2 LAN Servers TCP" -Direction Inbound -Protocol TCP -LocalPort 27015-27030 -Action Allow | Out-Null
        New-NetFirewallRule -DisplayName "CS2 LAN CSTV" -Direction Inbound -Protocol UDP -LocalPort 28015-28030 -Action Allow | Out-Null
        New-NetFirewallRule -DisplayName "CS2 LAN CSTV TCP" -Direction Inbound -Protocol TCP -LocalPort 28015-28030 -Action Allow | Out-Null
        Write-Host "Firewall opened for CS2 LAN (ports 27015-27030, CSTV 28015-28030)"

        if ($WebPort -gt 0) {
            New-NetFirewallRule -DisplayName "CS2 LAN Panel" -Direction Inbound -Protocol TCP -LocalPort $WebPort -Action Allow | Out-Null
            Write-Host "Firewall opened for web panel (port $WebPort)"
        }
    }
    "disable" {
        Remove-NetFirewallRule -DisplayName "CS2 LAN Servers" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN Servers TCP" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN CSTV" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN CSTV TCP" -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName "CS2 LAN Panel" -ErrorAction SilentlyContinue
        Write-Host "Firewall rules removed"
    }
}
