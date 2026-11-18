#requires -Version 7.2
# Toolhelp/NtQueryInformationProcess 不依赖 WMI/CIM 服务或远程管理权限。
if (-not ('MoeriseAndroid.WindowsProcesses' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
namespace MoeriseAndroid {
    public sealed class WindowsProcess {
        public int ProcessId;
        public int ParentProcessId;
        public string Name;
    }
    public static class WindowsProcesses {
        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        private struct Entry {
            public uint Size, Usage, Id;
            public IntPtr Heap;
            public uint Module, Threads, Parent;
            public int Priority;
            public uint Flags;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 260)] public string Name;
        }
        [StructLayout(LayoutKind.Sequential)]
        private struct UnicodeString {
            public ushort Length, MaximumLength;
            public IntPtr Buffer;
        }
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr CreateToolhelp32Snapshot(uint flags, uint id);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool Process32FirstW(IntPtr snapshot, ref Entry entry);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool Process32NextW(IntPtr snapshot, ref Entry entry);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr OpenProcess(uint access, bool inherit, int id);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool QueryFullProcessImageNameW(IntPtr process, uint flags, StringBuilder text, ref int size);
        [DllImport("kernel32.dll")] private static extern bool CloseHandle(IntPtr handle);
        [DllImport("ntdll.dll")]
        private static extern int NtQueryInformationProcess(IntPtr process, int kind, IntPtr buffer, int length, out int returned);
        public static WindowsProcess[] Snapshot() {
            var snapshot = CreateToolhelp32Snapshot(2, 0);
            if (snapshot == new IntPtr(-1)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            try {
                var result = new List<WindowsProcess>();
                var entry = new Entry { Size = (uint)Marshal.SizeOf<Entry>() };
                if (!Process32FirstW(snapshot, ref entry)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
                do {
                    result.Add(new WindowsProcess { ProcessId = (int)entry.Id, ParentProcessId = (int)entry.Parent, Name = entry.Name });
                } while (Process32NextW(snapshot, ref entry));
                return result.ToArray();
            } finally { CloseHandle(snapshot); }
        }
        public static string CommandLine(int id) {
            var handle = OpenProcess(0x1000, false, id);
            if (handle == IntPtr.Zero) return null;
            try {
                int size;
                NtQueryInformationProcess(handle, 60, IntPtr.Zero, 0, out size);
                if (size <= 0 || size > 1024 * 1024) return null;
                var buffer = Marshal.AllocHGlobal(size);
                try {
                    if (NtQueryInformationProcess(handle, 60, buffer, size, out size) < 0) return null;
                    var text = Marshal.PtrToStructure<UnicodeString>(buffer);
                    return Marshal.PtrToStringUni(text.Buffer, text.Length / 2);
                } finally { Marshal.FreeHGlobal(buffer); }
            } finally { CloseHandle(handle); }
        }
        public static string ImagePath(int id) {
            var handle = OpenProcess(0x1000, false, id);
            if (handle == IntPtr.Zero) return null;
            try {
                var text = new StringBuilder(32768); int size = text.Capacity;
                return QueryFullProcessImageNameW(handle, 0, text, ref size) ? text.ToString() : null;
            } finally { CloseHandle(handle); }
        }
    }
}
'@
}

function Get-MoeriseWindowsProcesses([string[]] $Names = @('java.exe', 'node.exe', 'moerise.exe')) {
    foreach ($entry in [MoeriseAndroid.WindowsProcesses]::Snapshot()) {
        if ($entry.Name -notin $Names) { continue }
        $process = Get-Process -Id $entry.ProcessId -ErrorAction SilentlyContinue
        if (-not $process) { continue }
        try { $ticks = $process.StartTime.ToUniversalTime().Ticks.ToString() } catch { $ticks = $null }
        [pscustomobject] @{
            ProcessId = $entry.ProcessId; ParentProcessId = $entry.ParentProcessId; Name = $entry.Name
            ProcessStartTicks = $ticks
            CommandLine = [MoeriseAndroid.WindowsProcesses]::CommandLine($entry.ProcessId)
            ExecutablePath = [MoeriseAndroid.WindowsProcesses]::ImagePath($entry.ProcessId)
        }
    }
}
