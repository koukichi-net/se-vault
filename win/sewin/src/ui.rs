//! The windows se-env shows: message boxes and the hidden-input dialog.

use crate::wide;
use core::ffi::c_void;
use std::cell::RefCell;
use windows_sys::Win32::Foundation::{HWND, LPARAM, LRESULT, RECT, WPARAM};
use windows_sys::Win32::Graphics::Gdi::{
    CreateFontIndirectW, DT_CALCRECT, DT_WORDBREAK, DeleteObject, DrawTextW, GetDC, HFONT,
    ReleaseDC, SelectObject,
};
use windows_sys::Win32::System::LibraryLoader::GetModuleHandleW;
use windows_sys::Win32::UI::HiDpi::{
    DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, GetDpiForSystem, SetProcessDpiAwarenessContext,
    SystemParametersInfoForDpi,
};
use windows_sys::Win32::UI::Input::KeyboardAndMouse::SetFocus;
use windows_sys::Win32::UI::WindowsAndMessaging::*;

/// Call once at the start of a program that shows windows.
pub fn init_dpi() {
    unsafe { SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2) };
}

fn scale(v: i32) -> i32 {
    let dpi = unsafe { GetDpiForSystem() } as i32;
    v * dpi / 96
}

fn message_font() -> HFONT {
    let dpi = unsafe { GetDpiForSystem() };
    let mut m: NONCLIENTMETRICSW = unsafe { core::mem::zeroed() };
    m.cbSize = core::mem::size_of::<NONCLIENTMETRICSW>() as u32;
    unsafe {
        SystemParametersInfoForDpi(
            SPI_GETNONCLIENTMETRICS,
            m.cbSize,
            (&mut m as *mut NONCLIENTMETRICSW).cast(),
            0,
            dpi,
        );
        CreateFontIndirectW(&m.lfMessageFont)
    }
}

fn set_font(h: HWND, font: HFONT) {
    unsafe { SendMessageW(h, WM_SETFONT, font as WPARAM, 1) };
}

/// Yes/no, warning icon, "no" is the default. True for yes.
pub fn confirm(title: &str, text: &str) -> bool {
    let (t, m) = (wide(title), wide(text));
    let r = unsafe {
        MessageBoxW(
            core::ptr::null_mut(),
            m.as_ptr(),
            t.as_ptr(),
            MB_YESNO | MB_ICONWARNING | MB_DEFBUTTON2 | MB_TOPMOST | MB_SETFOREGROUND,
        )
    };
    r == IDYES
}

/// A critical alert with an OK button. Returns when it is dismissed.
pub fn alert(title: &str, text: &str) {
    let (t, m) = (wide(title), wide(text));
    unsafe {
        MessageBoxW(
            core::ptr::null_mut(),
            m.as_ptr(),
            t.as_ptr(),
            MB_OK | MB_ICONERROR | MB_TOPMOST | MB_SETFOREGROUND,
        )
    };
}

fn register(class: &[u16], proc_: WNDPROC) {
    let wc = WNDCLASSEXW {
        cbSize: core::mem::size_of::<WNDCLASSEXW>() as u32,
        lpfnWndProc: proc_,
        hInstance: unsafe { GetModuleHandleW(core::ptr::null()) },
        hCursor: unsafe { LoadCursorW(core::ptr::null_mut(), IDC_ARROW) },
        hbrBackground: (15 + 1) as usize as *mut c_void, // COLOR_BTNFACE + 1
        lpszClassName: class.as_ptr(),
        ..unsafe { core::mem::zeroed() }
    };
    unsafe { RegisterClassExW(&wc) }; // fails harmlessly if already registered
}

#[allow(clippy::too_many_arguments)]
fn child(
    class: &str,
    text: &str,
    style: u32,
    x: i32,
    y: i32,
    w: i32,
    h: i32,
    parent: HWND,
    id: i32,
    font: HFONT,
) -> HWND {
    let (c, t) = (wide(class), wide(text));
    let hwnd = unsafe {
        CreateWindowExW(
            0,
            c.as_ptr(),
            t.as_ptr(),
            WS_CHILD | WS_VISIBLE | style,
            x,
            y,
            w,
            h,
            parent,
            id as isize as *mut c_void,
            GetModuleHandleW(core::ptr::null()),
            core::ptr::null(),
        )
    };
    set_font(hwnd, font);
    hwnd
}

/// Height of `text` wrapped to `width` pixels in `font`.
fn text_height(text: &str, width: i32, font: HFONT) -> i32 {
    let mut w: Vec<u16> = text.encode_utf16().collect();
    let mut r = RECT {
        left: 0,
        top: 0,
        right: width,
        bottom: 0,
    };
    unsafe {
        let dc = GetDC(core::ptr::null_mut());
        let old = SelectObject(dc, font);
        DrawTextW(
            dc,
            w.as_mut_ptr(),
            w.len() as i32,
            &mut r,
            DT_CALCRECT | DT_WORDBREAK,
        );
        SelectObject(dc, old);
        ReleaseDC(core::ptr::null_mut(), dc);
    }
    r.bottom - r.top
}

fn run_loop(hwnd: HWND) {
    let mut msg: MSG = unsafe { core::mem::zeroed() };
    while unsafe { GetMessageW(&mut msg, core::ptr::null_mut(), 0, 0) } > 0 {
        if hwnd.is_null() || unsafe { IsDialogMessageW(hwnd, &msg) } == 0 {
            unsafe {
                TranslateMessage(&msg);
                DispatchMessageW(&msg);
            }
        }
    }
}

// ---- hidden input --------------------------------------------------------------

#[derive(Default)]
struct SecretState {
    edit: HWND,
    value: Option<Vec<u16>>,
}

thread_local! {
    static SECRET: RefCell<SecretState> = RefCell::default();
}

const ID_EDIT: i32 = 100;

unsafe extern "system" fn secret_proc(hwnd: HWND, msg: u32, wp: WPARAM, lp: LPARAM) -> LRESULT {
    match msg {
        WM_COMMAND => {
            let id = (wp & 0xffff) as i32;
            if id == IDOK {
                SECRET.with(|s| {
                    let mut s = s.borrow_mut();
                    let n = unsafe { GetWindowTextLengthW(s.edit) };
                    let mut buf = vec![0u16; n as usize + 1];
                    let got = unsafe { GetWindowTextW(s.edit, buf.as_mut_ptr(), buf.len() as i32) };
                    buf.truncate(got as usize);
                    // Clear the control so the value does not stay in its buffer.
                    let empty = [0u16];
                    unsafe { SetWindowTextW(s.edit, empty.as_ptr()) };
                    s.value = Some(buf);
                });
                unsafe { DestroyWindow(hwnd) };
            } else if id == IDCANCEL {
                unsafe { DestroyWindow(hwnd) };
            }
            0
        }
        WM_CLOSE => {
            unsafe { DestroyWindow(hwnd) };
            0
        }
        WM_DESTROY => {
            unsafe { PostQuitMessage(0) };
            0
        }
        _ => unsafe { DefWindowProcW(hwnd, msg, wp, lp) },
    }
}

/// Asks for a value with hidden input. None when cancelled.
pub fn ask_secret(title: &str, label: &str) -> Option<String> {
    let class = wide("se-env-secret");
    register(&class, Some(secret_proc));
    let font = message_font();
    let (pad, width) = (scale(16), scale(440));
    let inner = width - 2 * pad;
    let label_h = text_height(label, inner, font).max(scale(20));
    let edit_y = pad + label_h + scale(10);
    let edit_h = scale(26);
    let btn_y = edit_y + edit_h + scale(16);
    let (btn_w, btn_h) = (scale(88), scale(28));
    let client_h = btn_y + btn_h + pad;

    let style = WS_CAPTION | WS_SYSMENU | WS_POPUP;
    let ex = WS_EX_TOPMOST | WS_EX_DLGMODALFRAME;
    let mut r = RECT {
        left: 0,
        top: 0,
        right: width,
        bottom: client_h,
    };
    unsafe { AdjustWindowRectEx(&mut r, style, 0, ex) };
    let (w, h) = (r.right - r.left, r.bottom - r.top);
    let (sw, sh) = unsafe { (GetSystemMetrics(SM_CXSCREEN), GetSystemMetrics(SM_CYSCREEN)) };
    let t = wide(title);
    let hwnd = unsafe {
        CreateWindowExW(
            ex,
            class.as_ptr(),
            t.as_ptr(),
            style,
            (sw - w) / 2,
            (sh - h) / 3,
            w,
            h,
            core::ptr::null_mut(),
            core::ptr::null_mut(),
            GetModuleHandleW(core::ptr::null()),
            core::ptr::null(),
        )
    };
    if hwnd.is_null() {
        return None;
    }
    child("STATIC", label, 0, pad, pad, inner, label_h, hwnd, -1, font);
    let edit = child(
        "EDIT",
        "",
        WS_BORDER | WS_TABSTOP | (ES_PASSWORD | ES_AUTOHSCROLL) as u32,
        pad,
        edit_y,
        inner,
        edit_h,
        hwnd,
        ID_EDIT,
        font,
    );
    child(
        "BUTTON",
        "OK",
        WS_TABSTOP | BS_DEFPUSHBUTTON as u32,
        width - pad - 2 * btn_w - scale(8),
        btn_y,
        btn_w,
        btn_h,
        hwnd,
        IDOK,
        font,
    );
    child(
        "BUTTON",
        "Cancel",
        WS_TABSTOP | BS_PUSHBUTTON as u32,
        width - pad - btn_w,
        btn_y,
        btn_w,
        btn_h,
        hwnd,
        IDCANCEL,
        font,
    );
    SECRET.with(|s| *s.borrow_mut() = SecretState { edit, value: None });
    unsafe {
        ShowWindow(hwnd, SW_SHOW);
        SetForegroundWindow(hwnd);
        SetFocus(edit);
    }
    run_loop(hwnd);
    unsafe { DeleteObject(font) };
    let mut v = SECRET.with(|s| s.borrow_mut().value.take())?;
    let out = String::from_utf16(&v).ok();
    v.iter_mut().for_each(|c| *c = 0);
    out
}
