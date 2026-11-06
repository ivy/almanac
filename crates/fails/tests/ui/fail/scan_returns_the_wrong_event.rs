use fails::*;

observed! {
    pub struct Other { pub id: u64 }
}

impl Event for Other {
    const KIND: &'static str = "other";
    fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {
        Ok(Self { id: p.field("/id")? })
    }
}

async fn lists_others(_: &Scanner) -> Result<Vec<Scanned<Other>>, StepError> {
    Ok(vec![])
}

#[workflow(on = Opened, key = "{repo}", intent = "x", may = [Await], runs_per_hour = 1, scan = lists_others, decide = no_effects)]
async fn wrong(_cx: &Cx, _event: &Opened) -> Result<Done, Halt> {
    Ok(Done { ok: true })
}

fn main() {}
