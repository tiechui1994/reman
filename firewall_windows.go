//go:build windows
// +build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// convertToUTF8 converts Windows command output from GBK/CP936 to UTF-8
func convertToUTF8(data []byte) (string, error) {
	// Try to decode as GBK (simplified Chinese)
	decoder := simplifiedchinese.GBK.NewDecoder()
	result, _, err := transform.Bytes(decoder, data)
	if err != nil {
		// If conversion fails, return as-is (might already be UTF-8 or other encoding)
		return string(data), nil
	}
	return string(result), nil
}

// addFirewallRule adds a firewall rule to allow incoming connections on the specified port
func addFirewallRule(port uint) error {
	// Check if running with administrator privileges
	elevated, err := isElevated()
	if err != nil {
		sysLogger.Printf("warning: failed to check administrator privileges: %v", err)
		return nil // Don't fail startup if we can't check privileges
	}
	if !elevated {
		sysLogger.Printf("warning: need administrator privileges to configure firewall for port %d", port)
		return nil // Don't fail startup, just log a warning
	}

	ruleName := fmt.Sprintf("reman-rpc-%d", port)
	portStr := fmt.Sprintf("%d", port)

	// Check if rule already exists
	checkCmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", fmt.Sprintf("name=%s", ruleName))
	output, err := checkCmd.CombinedOutput()
	if err == nil {
		outputStr, _ := convertToUTF8(output)
		if strings.Contains(outputStr, ruleName) {
			sysLogger.Printf("firewall rule for port %d already exists", port)
			return nil
		}
	}

	// Add the firewall rule using netsh
	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		fmt.Sprintf("name=%s", ruleName),
		"dir=in",
		"action=allow",
		"protocol=TCP",
		fmt.Sprintf("localport=%s", portStr),
		"description=Reman RPC Server Port",
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		outputStr, _ := convertToUTF8(output)
		sysLogger.Printf("warning: failed to add firewall rule for port %d: %v, output: %s", port, err, outputStr)
		return nil // Don't fail startup if firewall rule addition fails
	}

	sysLogger.Printf("added Windows firewall rule for port %d (rule: %s)", port, ruleName)
	return nil
}

// removeFirewallRule removes a firewall rule for the specified port
func removeFirewallRule(port uint) error {
	// Check if running with administrator privileges
	elevated, err := isElevated()
	if err != nil {
		return fmt.Errorf("failed to check administrator privileges: %w", err)
	}
	if !elevated {
		return fmt.Errorf("insufficient privileges")
	}

	ruleName := fmt.Sprintf("reman-rpc-%d", port)

	// Remove the firewall rule
	cmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		fmt.Sprintf("name=%s", ruleName),
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		outputStr, _ := convertToUTF8(output)
		// Rule might not exist, which is fine
		// Check for both Chinese and English error messages
		if strings.Contains(outputStr, "找不到") || strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "No rules match") || strings.Contains(outputStr, "找不到规则") {
			sysLogger.Printf("firewall rule %s does not exist", ruleName)
			return nil
		}
		return fmt.Errorf("failed to remove firewall rule: %v, output: %s", err, outputStr)
	}

	sysLogger.Printf("removed Windows firewall rule for port %d (rule: %s)", port, ruleName)
	return nil
}

