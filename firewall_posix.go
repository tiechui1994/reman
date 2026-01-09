//go:build !windows
// +build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// addFirewallRule adds a firewall rule to allow incoming connections on the specified port
func addFirewallRule(port uint) error {
	// Try different firewall management tools based on the system
	// First try firewalld (common on RHEL/CentOS/Fedora)
	if err := addFirewallRuleFirewalld(port); err == nil {
		return nil
	}

	// Then try ufw (common on Ubuntu/Debian)
	if err := addFirewallRuleUFW(port); err == nil {
		return nil
	}

	// Finally try iptables (fallback for most Linux systems)
	if err := addFirewallRuleIptables(port); err == nil {
		return nil
	}

	// If all methods fail, log a warning but don't fail the startup
	sysLogger.Printf("warning: failed to add firewall rule for port %d (firewall may need manual configuration)", port)
	return nil
}

// addFirewallRuleFirewalld adds a rule using firewalld
func addFirewallRuleFirewalld(port uint) error {
	// Check if firewalld is available
	if _, err := exec.LookPath("firewall-cmd"); err != nil {
		return fmt.Errorf("firewall-cmd not found")
	}

	// Check if running as root
	if os.Geteuid() != 0 {
		sysLogger.Printf("warning: need root privileges to configure firewalld for port %d", port)
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d/tcp", port)
	ruleName := fmt.Sprintf("reman-rpc-%d", port)

	// Check if rule already exists
	checkCmd := exec.Command("firewall-cmd", "--permanent", "--query-port", portStr)
	if err := checkCmd.Run(); err == nil {
		sysLogger.Printf("firewall rule for port %d already exists in firewalld", port)
		return nil
	}

	// Add the port
	cmd := exec.Command("firewall-cmd", "--permanent", "--add-port", portStr)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("firewall-cmd add-port failed: %v, output: %s", err, string(output))
	}

	// Reload firewall
	reloadCmd := exec.Command("firewall-cmd", "--reload")
	if output, err := reloadCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("firewall-cmd reload failed: %v, output: %s", err, string(output))
	}

	sysLogger.Printf("added firewalld rule for port %d (rule: %s)", port, ruleName)
	return nil
}

// addFirewallRuleUFW adds a rule using ufw
func addFirewallRuleUFW(port uint) error {
	// Check if ufw is available
	if _, err := exec.LookPath("ufw"); err != nil {
		return fmt.Errorf("ufw not found")
	}

	// Check if running as root
	if os.Geteuid() != 0 {
		sysLogger.Printf("warning: need root privileges to configure ufw for port %d", port)
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d/tcp", port)

	// Check if rule already exists
	checkCmd := exec.Command("ufw", "status", "numbered")
	output, err := checkCmd.CombinedOutput()
	if err == nil && strings.Contains(string(output), portStr) {
		sysLogger.Printf("firewall rule for port %d already exists in ufw", port)
		return nil
	}

	// Add the rule
	cmd := exec.Command("ufw", "allow", portStr, "comment", fmt.Sprintf("reman-rpc-%d", port))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ufw allow failed: %v, output: %s", err, string(output))
	}

	sysLogger.Printf("added ufw rule for port %d", port)
	return nil
}

// addFirewallRuleIptables adds a rule using iptables
func addFirewallRuleIptables(port uint) error {
	// Check if iptables is available
	if _, err := exec.LookPath("iptables"); err != nil {
		return fmt.Errorf("iptables not found")
	}

	// Check if running as root
	if os.Geteuid() != 0 {
		sysLogger.Printf("warning: need root privileges to configure iptables for port %d", port)
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d", port)

	// Check if rule already exists
	checkCmd := exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	if err := checkCmd.Run(); err == nil {
		sysLogger.Printf("firewall rule for port %d already exists in iptables", port)
		return nil
	}

	// Add the rule
	cmd := exec.Command("iptables", "-I", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables add rule failed: %v, output: %s", err, string(output))
	}

	sysLogger.Printf("added iptables rule for port %d", port)
	return nil
}

// removeFirewallRule removes a firewall rule for the specified port
func removeFirewallRule(port uint) error {
	// Try different firewall management tools
	removed := false

	// Try firewalld
	if err := removeFirewallRuleFirewalld(port); err == nil {
		removed = true
	}

	// Try ufw
	if err := removeFirewallRuleUFW(port); err == nil {
		removed = true
	}

	// Try iptables
	if err := removeFirewallRuleIptables(port); err == nil {
		removed = true
	}

	if !removed {
		sysLogger.Printf("warning: failed to remove firewall rule for port %d", port)
	}

	return nil
}

// removeFirewallRuleFirewalld removes a rule from firewalld
func removeFirewallRuleFirewalld(port uint) error {
	if _, err := exec.LookPath("firewall-cmd"); err != nil {
		return fmt.Errorf("firewall-cmd not found")
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d/tcp", port)
	cmd := exec.Command("firewall-cmd", "--permanent", "--remove-port", portStr)
	if err := cmd.Run(); err != nil {
		return err
	}

	reloadCmd := exec.Command("firewall-cmd", "--reload")
	reloadCmd.Run() // Ignore error on reload

	sysLogger.Printf("removed firewalld rule for port %d", port)
	return nil
}

// removeFirewallRuleUFW removes a rule from ufw
func removeFirewallRuleUFW(port uint) error {
	if _, err := exec.LookPath("ufw"); err != nil {
		return fmt.Errorf("ufw not found")
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d/tcp", port)
	cmd := exec.Command("ufw", "delete", "allow", portStr)
	if err := cmd.Run(); err != nil {
		return err
	}

	sysLogger.Printf("removed ufw rule for port %d", port)
	return nil
}

// removeFirewallRuleIptables removes a rule from iptables
func removeFirewallRuleIptables(port uint) error {
	if _, err := exec.LookPath("iptables"); err != nil {
		return fmt.Errorf("iptables not found")
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("insufficient privileges")
	}

	portStr := fmt.Sprintf("%d", port)
	cmd := exec.Command("iptables", "-D", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		return err
	}

	sysLogger.Printf("removed iptables rule for port %d", port)
	return nil
}
