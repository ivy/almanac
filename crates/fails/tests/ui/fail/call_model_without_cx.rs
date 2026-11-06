use fails::*;

async fn f(h: &ScriptedHarness) {
    let pass = Pass(());
    let _ = h.run(&pass, "p", todo!(), todo!()).await;
}
fn main() {}
