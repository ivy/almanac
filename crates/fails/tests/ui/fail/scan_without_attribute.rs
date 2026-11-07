use fails::*;

clean! {
    pub struct Nothing { pub ok: bool }
}

observed! {
    pub struct Thing { pub id: Slug }
}

impl Event for Thing {
    const KIND: &'static str = "things.found";
    fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {
        Ok(Self { id: p.field("/id")? })
    }
}

async fn plain(_scanner: &Scanner) -> Result<Vec<Scanned<Thing>>, StepError> {
    Ok(vec![])
}

fn policy(_: &Nothing) -> Vec<Effect> {
    vec![]
}

#[workflow(
    on = Thing,
    key = "{id}",
    intent = "Refused: its scan's needs would go unchecked.",
    may = [Await],
    runs_per_hour = 1,
    scan = plain,
    decide = policy,
)]
async fn watch(_cx: &Cx, _event: &Thing) -> Result<Nothing, Halt> {
    Ok(Nothing { ok: true })
}

fn main() {}
