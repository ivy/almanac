//! `#[workflow]`: turns an async function plus declared metadata into a `Workflow` impl.
use proc_macro::TokenStream;
use proc_macro2::{Span, TokenStream as Tokens};
use quote::{ToTokens, format_ident, quote, quote_spanned};
use syn::parse::Parser;
use syn::punctuated::Punctuated;
use syn::spanned::Spanned;
use syn::visit::{self, Visit};
use syn::{
    Error, Expr, ExprArray, ExprCall, ExprLit, ExprMethodCall, ExprPath, Field, Fields, FnArg,
    GenericArgument, Ident, ItemFn, ItemStruct, Lit, LitInt, LitStr, MetaNameValue, Pat,
    PathArguments, Result, ReturnType, Token, Type,
};

#[proc_macro_attribute]
pub fn workflow(attr: TokenStream, item: TokenStream) -> TokenStream {
    expand(attr.into(), item.into())
        .unwrap_or_else(Error::into_compile_error)
        .into()
}

/// Binds a struct to a Markdown prompt in `prompts/`. Every `{{ variable }}` must be a field
/// and every field must be used, checked at compile time. Fields must be `Clean + Display`.
#[proc_macro_attribute]
pub fn prompt(attr: TokenStream, item: TokenStream) -> TokenStream {
    expand_prompt(attr.into(), item.into())
        .unwrap_or_else(Error::into_compile_error)
        .into()
}

enum Piece {
    Text(String),
    Var(String),
}

/// `{{ name }}` is a variable and `\{{` is a literal `{{`. Anything else in braces is an error,
/// so a typo in a template cannot quietly reach a model as text.
fn parse_template(text: &str) -> std::result::Result<Vec<Piece>, String> {
    let line = |at: usize| text[..at].matches('\n').count() + 1;
    let (mut pieces, mut literal, mut at) = (Vec::new(), String::new(), 0);
    while let Some(found) = text[at..].find("{{") {
        let open = at + found;
        if text[..open].ends_with('\\') {
            literal.push_str(&text[at..open - 1]);
            literal.push_str("{{");
            at = open + 2;
            continue;
        }
        literal.push_str(&text[at..open]);
        let close = text[open..]
            .find("}}")
            .ok_or(format!("line {}: unclosed `{{{{`", line(open)))?
            + open;
        let name = text[open + 2..close].trim();
        let valid = name
            .chars()
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
            && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_');
        if !valid {
            return Err(format!(
                "line {}: `{{{{ {name} }}}}` is not a variable name",
                line(open)
            ));
        }
        pieces.push(Piece::Text(std::mem::take(&mut literal)));
        pieces.push(Piece::Var(name.to_string()));
        at = close + 2;
    }
    literal.push_str(&text[at..]);
    pieces.push(Piece::Text(literal));
    Ok(pieces)
}

fn expand_prompt(attr: Tokens, item: Tokens) -> Result<Tokens> {
    let input: ItemStruct = syn::parse2(item)?;
    let entry: Expr = syn::parse2(attr)?;
    let (source, text, track) = match entry {
        Expr::Lit(ExprLit {
            lit: Lit::Str(file),
            ..
        }) => {
            let name = file.value();
            if name.contains("..") || name.starts_with('/') {
                return Err(Error::new(
                    file.span(),
                    "a prompt is a path inside `prompts/`",
                ));
            }
            let root =
                std::env::var("CARGO_MANIFEST_DIR").map_err(|e| Error::new(file.span(), e))?;
            let path = format!("{root}/prompts/{name}");
            let text = std::fs::read_to_string(&path)
                .map_err(|e| Error::new(file.span(), format!("cannot read prompts/{name}: {e}")))?;
            (
                name,
                text,
                quote! { const _: &str = ::std::include_str!(#path); },
            )
        }
        Expr::Assign(assign) if matches!(&*assign.left, Expr::Path(p) if p.path.is_ident("text")) => {
            (
                "inline".to_string(),
                string(*assign.right)?.value(),
                quote! {},
            )
        }
        other => {
            return Err(Error::new_spanned(
                other,
                "expected a file name, like `#[prompt(\"reviewer.md\")]`",
            ));
        }
    };
    let pieces = parse_template(&text)
        .map_err(|e| Error::new(Span::call_site(), format!("prompts/{source}: {e}")))?;

    let fields: Vec<&Field> = match &input.fields {
        Fields::Named(named) => named.named.iter().collect(),
        Fields::Unit => vec![],
        Fields::Unnamed(_) => {
            return Err(Error::new_spanned(
                &input.fields,
                "a prompt has named fields",
            ));
        }
    };
    let names: Vec<String> = fields
        .iter()
        .filter_map(|f| f.ident.as_ref())
        .map(Ident::to_string)
        .collect();
    let vars: Vec<&String> = pieces
        .iter()
        .filter_map(|p| if let Piece::Var(v) = p { Some(v) } else { None })
        .collect();

    for var in &vars {
        if !names.contains(var) {
            let message = format!(
                "prompts/{source} uses `{{{{ {var} }}}}`, but `{}` has no field `{var}`",
                input.ident
            );
            return Err(Error::new_spanned(&input.ident, message));
        }
    }
    for field in &fields {
        let name = field
            .ident
            .as_ref()
            .map(Ident::to_string)
            .unwrap_or_default();
        if !vars.contains(&&name) {
            return Err(Error::new_spanned(
                field,
                format!("prompts/{source} never uses `{{{{ {name} }}}}`"),
            ));
        }
    }

    let writes = pieces.iter().map(|piece| match piece {
        Piece::Text(t) => quote! { out.push_str(#t); },
        Piece::Var(v) => {
            let field = format_ident!("{}", v);
            quote! { out.push_str(&::std::string::ToString::to_string(&self.#field)); }
        }
    });
    let checks = fields.iter().map(|f| {
        let ty = &f.ty;
        quote_spanned! { ty.span()=> check::<#ty>(); }
    });
    let declared = dedupe_strings(vars.iter().map(|v| v.to_string()).collect());
    let name = &input.ident;
    Ok(quote! {
        #input

        #track

        const _: fn() = || {
            fn check<T: ::almanac::taint::Clean + ::std::fmt::Display>() {}
            #(#checks)*
        };

        impl ::almanac::agent::Template for #name {
            const SOURCE: &'static str = #source;
            const VARS: &'static [&'static str] = &[#(#declared),*];

            fn render(&self) -> ::std::string::String {
                let mut out = ::std::string::String::new();
                #(#writes)*
                out
            }
        }
    })
}

fn dedupe_strings(mut items: Vec<String>) -> Vec<String> {
    let mut seen = Vec::new();
    items.retain(|item| {
        let fresh = !seen.contains(item);
        seen.push(item.clone());
        fresh
    });
    items
}

#[derive(Default)]
struct Args {
    on: Option<ExprPath>,
    key: Option<LitStr>,
    intent: Option<LitStr>,
    may: Vec<Ident>,
    runs_per_hour: Option<LitInt>,
    decide: Option<ExprPath>,
    scan: Option<ExprPath>,
    when: Option<ExprPath>,
    subjects: Vec<ExprCall>,
    links: Vec<ExprCall>,
}

fn path(value: Expr) -> Result<ExprPath> {
    match value {
        Expr::Path(p) => Ok(p),
        other => Err(Error::new_spanned(other, "expected a path")),
    }
}

fn string(value: Expr) -> Result<LitStr> {
    match value {
        Expr::Lit(ExprLit {
            lit: Lit::Str(s), ..
        }) => Ok(s),
        other => Err(Error::new_spanned(other, "expected a string")),
    }
}

fn list(value: Expr) -> Result<Vec<Expr>> {
    match value {
        Expr::Array(ExprArray { elems, .. }) => Ok(elems.into_iter().collect()),
        other => Err(Error::new_spanned(other, "expected a list like `[a, b]`")),
    }
}

fn calls(value: Expr) -> Result<Vec<ExprCall>> {
    list(value)?
        .into_iter()
        .map(|e| match e {
            Expr::Call(c) => Ok(c),
            other => Err(Error::new_spanned(
                other,
                "expected a subject like `repo(repo)`",
            )),
        })
        .collect()
}

fn parse_args(attr: Tokens) -> Result<Args> {
    let mut args = Args::default();
    for entry in Punctuated::<MetaNameValue, Token![,]>::parse_terminated.parse2(attr)? {
        let name = entry
            .path
            .get_ident()
            .map(Ident::to_string)
            .unwrap_or_default();
        let value = entry.value;
        match name.as_str() {
            "on" => args.on = Some(path(value)?),
            "key" => args.key = Some(string(value)?),
            "intent" => args.intent = Some(string(value)?),
            "decide" => args.decide = Some(path(value)?),
            "scan" => args.scan = Some(path(value)?),
            "when" => args.when = Some(path(value)?),
            "subjects" => args.subjects = calls(value)?,
            "links" => args.links = calls(value)?,
            "may" => {
                for kind in list(value)? {
                    let p = path(kind)?;
                    let ident = p.path.get_ident().cloned();
                    args.may.push(
                        ident.ok_or_else(|| Error::new_spanned(p, "expected an effect kind"))?,
                    );
                }
            }
            "runs_per_hour" => match value {
                Expr::Lit(ExprLit {
                    lit: Lit::Int(n), ..
                }) => args.runs_per_hour = Some(n),
                other => return Err(Error::new_spanned(other, "expected a number")),
            },
            _ => {
                return Err(Error::new_spanned(
                    entry.path,
                    format!("unknown workflow option `{name}`"),
                ));
            }
        }
    }
    Ok(args)
}

fn required<T>(value: Option<T>, name: &str) -> Result<T> {
    value.ok_or_else(|| {
        Error::new(
            Span::call_site(),
            format!("`#[workflow]` needs `{name} = ...`"),
        )
    })
}

/// `"{repo}:{head_branch}"` becomes a format string plus field reads. A field that does not
/// exist, or holds `Untrusted` text with no `Display`, is an ordinary compile error.
fn key_expr(key: &LitStr) -> Result<Tokens> {
    let text = key.value();
    let (mut format, mut fields, mut rest) = (String::new(), Vec::new(), text.as_str());
    while let Some(open) = rest.find('{') {
        format.push_str(&rest[..open]);
        let close = rest[open..]
            .find('}')
            .ok_or_else(|| Error::new(key.span(), "unclosed `{` in key"))?
            + open;
        let field: Expr = syn::parse_str(&rest[open + 1..close])
            .map_err(|_| Error::new(key.span(), "expected a field name in key"))?;
        fields.push(field);
        format.push_str("{}");
        rest = &rest[close + 1..];
    }
    format.push_str(rest);
    Ok(quote! { ::std::format!(#format, #(__event.#fields),*) })
}

fn subject_exprs(calls: &[ExprCall], root: &Ident) -> Vec<Tokens> {
    calls
        .iter()
        .map(|call| {
            let (ctor, args) = (&call.func, call.args.iter());
            quote! { ::almanac::workflow::Subject::#ctor(#(&#root.#args),*) }
        })
        .collect()
}

/// Finds what the function body uses through its context, so the manifest and the startup
/// check need no hand-kept list. Calls made in helpers are not seen: those fail at run time.
struct Uses<'a> {
    cx: &'a Ident,
    agents: Vec<Expr>,
    needs: Vec<Type>,
    screens: bool,
}

impl Uses<'_> {
    fn on_cx(&self, call: &ExprMethodCall) -> bool {
        matches!(&*call.receiver, Expr::Path(p) if p.path.is_ident(self.cx))
    }
}

impl<'ast> Visit<'ast> for Uses<'_> {
    fn visit_expr_method_call(&mut self, call: &'ast ExprMethodCall) {
        if self.on_cx(call) {
            match call.method.to_string().as_str() {
                "agent" => self.agents.extend(call.args.first().cloned()),
                "screen" => self.screens = true,
                "get" => {
                    if let Some(GenericArgument::Type(t)) =
                        call.turbofish.as_ref().and_then(|g| g.args.first())
                    {
                        self.needs.push(t.clone());
                    }
                }
                _ => {}
            }
        }
        visit::visit_expr_method_call(self, call);
    }
}

fn dedupe<T: ToTokens>(items: Vec<T>) -> Vec<T> {
    let mut seen = Vec::new();
    items
        .into_iter()
        .filter(|item| {
            let text = item.to_token_stream().to_string();
            let fresh = !seen.contains(&text);
            seen.push(text);
            fresh
        })
        .collect()
}

fn facts_type(function: &ItemFn) -> Result<Type> {
    let problem = || {
        Error::new_spanned(
            &function.sig.output,
            "a workflow returns `Result<Facts, Halt>`",
        )
    };
    let ReturnType::Type(_, output) = &function.sig.output else {
        return Err(problem());
    };
    let Type::Path(result) = &**output else {
        return Err(problem());
    };
    let segment = result.path.segments.last().ok_or_else(problem)?;
    let PathArguments::AngleBracketed(generics) = &segment.arguments else {
        return Err(problem());
    };
    match generics.args.first() {
        Some(GenericArgument::Type(t)) => Ok(t.clone()),
        _ => Err(problem()),
    }
}

fn cx_param(function: &ItemFn) -> Result<Ident> {
    let problem =
        || Error::new_spanned(&function.sig, "a workflow takes `(cx: &Cx, event: &Event)`");
    match function.sig.inputs.first() {
        Some(FnArg::Typed(arg)) => match &*arg.pat {
            Pat::Ident(p) => Ok(p.ident.clone()),
            _ => Err(problem()),
        },
        _ => Err(problem()),
    }
}

fn camel(name: &str) -> String {
    name.split('_')
        .map(|part| {
            let mut chars = part.chars();
            chars.next().map_or(String::new(), |first| {
                first.to_uppercase().chain(chars).collect()
            })
        })
        .collect()
}

fn expand(attr: Tokens, item: Tokens) -> Result<Tokens> {
    let function: ItemFn = syn::parse2(item)?;
    if function.sig.asyncness.is_none() {
        return Err(Error::new_spanned(
            &function.sig,
            "a workflow must be an `async fn`",
        ));
    }
    let args = parse_args(attr)?;
    let on = required(args.on, "on")?;
    let key = key_expr(&required(args.key, "key")?)?;
    let intent = required(args.intent, "intent")?;
    let runs_per_hour = required(args.runs_per_hour, "runs_per_hour")?;
    let decide = required(args.decide, "decide")?;
    if args.may.is_empty() {
        return Err(Error::new(
            Span::call_site(),
            "`#[workflow]` needs `may = [...]`",
        ));
    }
    let may = args.may;

    let facts = facts_type(&function)?;
    let cx = cx_param(&function)?;
    let mut uses = Uses {
        cx: &cx,
        agents: vec![],
        needs: vec![],
        screens: false,
    };
    uses.visit_block(&function.block);
    let (agents, needs, screens) = (dedupe(uses.agents), dedupe(uses.needs), uses.screens);
    let mut requirements: Vec<Tokens> = needs
        .iter()
        .map(|t| quote! { ::almanac::providers::Requirement::of::<#t>() })
        .collect();
    if screens {
        requirements.push(
            quote! { ::almanac::providers::Requirement::of::<dyn ::almanac::agent::Classifier>() },
        );
    }

    let (event, facts_arg) = (format_ident!("__event"), format_ident!("__facts"));
    let subjects = subject_exprs(&args.subjects, &event);
    let links = subject_exprs(&args.links, &facts_arg);

    let scan = args.scan.as_ref().map(|scan| {
        quote! {
            const SCANS: bool = true;

            async fn scan(
                &self,
                scanner: &::almanac::workflow::Scanner,
            ) -> ::std::result::Result<
                ::std::vec::Vec<::almanac::workflow::Scanned<#on>>,
                ::almanac::cx::StepError,
            > {
                #scan(scanner).await
            }
        }
    });

    let when = args.when.as_ref().map(|when| {
        quote! {
            fn wanted(event: &#on) -> bool {
                #when(event)
            }
        }
    });

    let name = &function.sig.ident;
    let visibility = &function.vis;
    let label = name.to_string().replace('_', "-");
    let unit = format_ident!("{}", camel(&name.to_string()));

    Ok(quote! {
        #function

        #visibility struct #unit;

        impl ::almanac::workflow::Workflow for #unit {
            type Event = #on;
            type Facts = #facts;

            const NAME: &'static str = #label;
            const INTENT: &'static str = #intent;
            const MAY: &'static [::almanac::effect::EffectKind] = &[#(::almanac::effect::EffectKind::#may),*];
            const RUNS_PER_HOUR: u32 = #runs_per_hour;

            #scan
            #when

            fn key(#event: &#on) -> ::std::string::String {
                #key
            }

            #[allow(unused_variables)]
            fn subjects(#event: &#on) -> ::std::vec::Vec<::almanac::workflow::Subject> {
                ::std::vec![#(#subjects),*]
            }

            #[allow(unused_variables)]
            fn links(#facts_arg: &#facts) -> ::std::vec::Vec<::almanac::workflow::Subject> {
                ::std::vec![#(#links),*]
            }

            fn agents(&self) -> ::std::vec::Vec<::almanac::agent::AgentInfo> {
                ::std::vec![#(#agents.info()),*]
            }

            fn requires(&self) -> ::std::vec::Vec<::almanac::providers::Requirement> {
                ::std::vec![#(#requirements),*]
            }

            async fn gather(
                &self,
                cx: &::almanac::cx::Cx,
                event: &#on,
            ) -> ::std::result::Result<#facts, ::almanac::cx::Halt> {
                #name(cx, event).await
            }

            fn decide(facts: &#facts) -> ::std::vec::Vec<::almanac::effect::Effect> {
                #decide(facts)
            }
        }
    })
}
