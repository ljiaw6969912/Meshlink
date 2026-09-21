package deviceidentity

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LocalMAC returns a physical Ethernet/Wi-Fi adapter MAC, or empty when Windows
// cannot identify a usable adapter. It launches no processes and retains no
// mutable cache. Hardware removal/addition can change the chosen address.
func LocalMAC() string {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil || table == nil {
		return ""
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	adapters := make([]adapter, 0, table.NumEntries)
	for _, row := range unsafe.Slice(&table.Table[0], int(table.NumEntries)) {
		if row.PhysicalAddressLength != 6 {
			continue
		}
		adapters = append(adapters, adapterFromRow(row))
	}
	return selectMAC(adapters)
}

func adapterFromRow(row windows.MibIfRow2) adapter {
	// HardwareInterface is bit 0; FilterInterface is bit 1 of the Windows
	// MIB_IF_ROW2 InterfaceAndOperStatusFlags bitfield.
	return adapter{
		name:      windows.UTF16ToString(row.Alias[:]) + " " + windows.UTF16ToString(row.Description[:]),
		hardware:  row.InterfaceAndOperStatusFlags&1 != 0,
		filter:    row.InterfaceAndOperStatusFlags&2 != 0,
		kind:      row.Type,
		permanent: net.HardwareAddr(row.PermanentPhysicalAddress[:6]).String(),
		current:   net.HardwareAddr(row.PhysicalAddress[:6]).String(),
	}
}
