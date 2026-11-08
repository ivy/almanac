//! Whether a narrower, which may read hostile text, can be turned into a way to read all of it.
//! Without `Tainted::Reshaped`, the first attack here recovers the whole text.
mod common;
use almanac::prelude::*;
use common::*;

/// Eight narrowers, each fixed in the source, that together read the first byte of what they get.
const BITS: &str = r#"
(narrower bit0 [t] (= 1 (% (string.byte t 1) 2)))
(narrower bit1 [t] (= 1 (% (// (string.byte t 1) 2) 2)))
(narrower bit2 [t] (= 1 (% (// (string.byte t 1) 4) 2)))
(narrower bit3 [t] (= 1 (% (// (string.byte t 1) 8) 2)))
(narrower bit4 [t] (= 1 (% (// (string.byte t 1) 16) 2)))
(narrower bit5 [t] (= 1 (% (// (string.byte t 1) 32) 2)))
(narrower bit6 [t] (= 1 (% (// (string.byte t 1) 64) 2)))
(narrower bit7 [t] (= 1 (% (// (string.byte t 1) 128) 2)))
(fn byte-of [text]
  (var byte 0)
  (when (taint.narrow text :bit0) (set byte (+ byte 1)))
  (when (taint.narrow text :bit1) (set byte (+ byte 2)))
  (when (taint.narrow text :bit2) (set byte (+ byte 4)))
  (when (taint.narrow text :bit3) (set byte (+ byte 8)))
  (when (taint.narrow text :bit4) (set byte (+ byte 16)))
  (when (taint.narrow text :bit5) (set byte (+ byte 32)))
  (when (taint.narrow text :bit6) (set byte (+ byte 64)))
  (when (taint.narrow text :bit7) (set byte (+ byte 128)))
  byte)
(fn steal [title]
  (var stolen "")
  (for [from 28 1 -1]
    (set stolen (.. stolen (string.char (byte-of (taint.tail title from))))))
  stolen)
"#;

#[tokio::test]
async fn shifted_text_does_not_turn_a_fixed_narrower_into_a_reader() {
    let ran = run(Probe {
        top: BITS,
        gather: "{:stolen (steal event.title)}",
        policy: "[(effect.await facts.stolen)]",
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("reshaped"), "{}", ran.error());
    assert!(!ran.visible().contains(HOSTILE), "{}", ran.visible());
}

#[tokio::test]
async fn an_index_carried_in_a_clean_prefix_is_refused_the_same_way() {
    let ran = run(Probe {
        top: r#"(narrower at-index [t] (= 1 (% (string.byte t (tonumber (t:match "^%d+"))) 2)))"#,
        gather: r#"{:bit (taint.narrow (taint.concat "3|" event.title) :at-index)}"#,
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("reshaped"), "{}", ran.error());
}

#[tokio::test]
async fn reshaped_text_stays_reshaped_through_the_run_log() {
    let ran = run(Probe {
        top: "(narrower any [t] true)",
        gather: r#"(let [shifted (taint.tail event.title 5)
                         kept (cx.observe :kept #{:shifted shifted})]
                     {:bit (taint.narrow kept.shifted :any)})"#,
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("reshaped"), "{}", ran.error());
}

#[tokio::test]
async fn a_script_cannot_relabel_tainted_content_with_the_logs_markers() {
    let ran = run(Probe {
        top: "(narrower any [t] true)",
        gather: r#"(let [shifted (taint.tail event.title 5)
                         kept (cx.observe :kept #{"$kind" :answer "$untrusted" shifted})]
                     {:bit (taint.narrow kept :any)})"#,
        ..Probe::default()
    })
    .await;
    assert!(ran.error().contains("reserved"), "{}", ran.error());
}

#[tokio::test]
async fn a_narrower_can_still_leak_what_it_is_written_to_leak() {
    let ran = run(Probe {
        top: "(narrower low-bit [t] (= 1 (% (string.byte t 1) 2)))",
        gather: "{:bit (taint.narrow event.title :low-bit)}",
        policy: r#"[(effect.await (tostring facts.bit))]"#,
        ..Probe::default()
    })
    .await;
    // `I` is 0x49. One bit per narrower per value is the same bound a Rust `fn` has.
    assert!(ran.visible().contains("true"), "{}", ran.visible());
}

/// The same attack in Rust, against the real `Untrusted`. A non-capturing `fn` cannot hold a
/// local, but `join` lets a run choose what it reads by putting an index in the clean prefix.
#[test]
fn rust_join_and_narrow_read_the_text_too() {
    #[allow(clippy::ptr_arg)]
    fn bit(text: &String) -> bool {
        let (index, rest) = text.split_once('|').unwrap();
        let (at, shift) = index.split_once(',').unwrap();
        let (at, shift): (usize, u32) = (at.parse().unwrap(), shift.parse().unwrap());
        rest.as_bytes()[at] >> shift & 1 == 1
    }
    let hostile = Untrusted::new(HOSTILE.to_string());
    let mut stolen = vec![];
    for at in 0..HOSTILE.len() {
        let mut byte = 0u8;
        for shift in 0..8 {
            let probe = Untrusted::join(&format!("{at},{shift}|"), &hostile);
            if probe.narrow(bit) {
                byte |= 1 << shift;
            }
        }
        stolen.push(byte);
    }
    assert_eq!(String::from_utf8(stolen).unwrap(), HOSTILE);
}
