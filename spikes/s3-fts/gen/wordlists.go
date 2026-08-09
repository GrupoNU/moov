package main

// Vocabulary for the synthetic corpus. Two wordlists (Spanish / English) of
// common words, drawn Zipfian so the token frequency distribution resembles a
// real mailbox: a handful of words in nearly every message, a long tail that
// appears a few times in 5M.
//
// These are deliberately plain words — the "rare token" behaviour of the
// benchmark comes from the tail of the Zipf draw plus the proper-noun and
// code generators in main.go (invoice numbers, order codes, URLs, phones).

var spanishWords = []string{
	"de", "la", "que", "el", "en", "y", "a", "los", "se", "del", "las", "un", "por", "con", "no",
	"una", "su", "para", "es", "al", "lo", "como", "mas", "pero", "sus", "le", "ya", "o", "este",
	"si", "porque", "esta", "entre", "cuando", "muy", "sin", "sobre", "tambien", "me", "hasta",
	"hay", "donde", "quien", "desde", "todo", "nos", "durante", "todos", "uno", "les", "ni",
	"contra", "otros", "ese", "eso", "ante", "ellos", "e", "esto", "mi", "antes", "algunos",
	"que", "unos", "yo", "otro", "otras", "otra", "el", "tanto", "esa", "estos", "mucho",
	"quienes", "nada", "muchos", "cual", "poco", "ella", "estar", "estas", "algunas", "algo",
	"nosotros", "mi", "mis", "tu", "te", "ti", "tus", "ellas", "nosotras", "vosotros", "vosotras",
	"buenos", "dias", "tardes", "noches", "hola", "gracias", "saludos", "cordiales", "atentamente",
	"estimado", "estimada", "estimados", "adjunto", "adjunta", "adjuntamos", "envio", "enviamos",
	"reunion", "reuniones", "agenda", "agendar", "confirmar", "confirmado", "confirmacion",
	"presupuesto", "presupuestos", "factura", "facturas", "facturacion", "pago", "pagos",
	"pagar", "cobro", "cobranza", "vencimiento", "vence", "vencido", "saldo", "cuenta",
	"cuentas", "cliente", "clientes", "proveedor", "proveedores", "contrato", "contratos",
	"propuesta", "propuestas", "informe", "informes", "reporte", "reportes", "resumen",
	"documento", "documentos", "archivo", "archivos", "planilla", "carpeta", "version",
	"proyecto", "proyectos", "tarea", "tareas", "pendiente", "pendientes", "urgente",
	"importante", "prioridad", "plazo", "plazos", "entrega", "entregas", "avance", "avances",
	"equipo", "equipos", "area", "areas", "departamento", "gerencia", "direccion", "oficina",
	"empresa", "empresas", "compania", "negocio", "negocios", "venta", "ventas", "vender",
	"compra", "compras", "comprar", "pedido", "pedidos", "orden", "ordenes", "envio", "envios",
	"remito", "remitos", "stock", "deposito", "logistica", "transporte", "flete",
	"precio", "precios", "costo", "costos", "descuento", "descuentos", "impuesto", "impuestos",
	"iva", "total", "subtotal", "monto", "importe", "moneda", "pesos", "dolares", "euros",
	"banco", "bancaria", "transferencia", "cheque", "efectivo", "tarjeta", "credito", "debito",
	"contabilidad", "contable", "balance", "ejercicio", "cierre", "auditoria", "auditor",
	"legal", "abogado", "juridico", "clausula", "acuerdo", "convenio", "firma", "firmado",
	"soporte", "servicio", "servicios", "sistema", "sistemas", "aplicacion", "plataforma",
	"usuario", "usuarios", "acceso", "clave", "contrasena", "permiso", "permisos", "error",
	"errores", "falla", "fallas", "incidente", "ticket", "reclamo", "consulta", "consultas",
	"respuesta", "respuestas", "solucion", "problema", "problemas", "prueba", "pruebas",
	"desarrollo", "produccion", "servidor", "servidores", "base", "datos", "red", "correo",
	"mensaje", "mensajes", "llamada", "llamadas", "telefono", "contacto", "contactos",
	"disponible", "disponibilidad", "horario", "horarios", "semana", "semanas", "mes", "meses",
	"ano", "anos", "dia", "dias", "hora", "horas", "minuto", "minutos", "hoy", "manana", "ayer",
	"lunes", "martes", "miercoles", "jueves", "viernes", "sabado", "domingo",
	"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre",
	"octubre", "noviembre", "diciembre", "trimestre", "semestre", "anual", "mensual",
	"necesito", "necesitamos", "podemos", "puede", "pueden", "podria", "quisiera", "quería",
	"favor", "amable", "posible", "posibilidad", "opcion", "opciones", "alternativa",
	"revisar", "revision", "revisado", "aprobar", "aprobacion", "aprobado", "rechazado",
	"actualizar", "actualizacion", "actualizado", "modificar", "cambio", "cambios",
	"coordinar", "coordinacion", "organizar", "planificar", "planificacion", "estrategia",
	"objetivo", "objetivos", "meta", "metas", "resultado", "resultados", "indicador",
	"crecimiento", "mejora", "mejoras", "calidad", "eficiencia", "productividad",
	"capacitacion", "curso", "cursos", "taller", "evento", "eventos", "presentacion",
	"marketing", "campana", "publicidad", "comunicacion", "prensa", "redes", "contenido",
	"personal", "recursos", "humanos", "empleado", "empleados", "sueldo", "nomina",
	"vacaciones", "licencia", "ausencia", "asistencia", "ingreso", "egreso", "renuncia",
	"seguro", "seguros", "poliza", "cobertura", "siniestro", "riesgo", "riesgos",
	"obra", "obras", "instalacion", "instalaciones", "mantenimiento", "reparacion",
	"material", "materiales", "herramienta", "herramientas", "insumo", "insumos",
	"medida", "medidas", "cantidad", "unidad", "unidades", "metro", "metros", "kilo",
	"norte", "sur", "este", "oeste", "centro", "zona", "region", "provincia", "ciudad",
	"calle", "avenida", "numero", "piso", "codigo", "postal", "direccion", "domicilio",
}

var englishWords = []string{
	"the", "of", "and", "to", "in", "a", "is", "that", "for", "it", "with", "as", "was", "on",
	"be", "at", "by", "this", "have", "from", "or", "one", "had", "not", "but", "what", "all",
	"were", "we", "when", "your", "can", "there", "use", "an", "each", "which", "she", "do",
	"how", "their", "if", "will", "up", "other", "about", "out", "many", "then", "them",
	"these", "so", "some", "her", "would", "make", "like", "him", "into", "time", "has", "look",
	"two", "more", "write", "go", "see", "number", "no", "way", "could", "people", "my", "than",
	"first", "been", "call", "who", "its", "now", "find", "long", "down", "day", "did", "get",
	"come", "made", "may", "part", "over", "new", "sound", "take", "only", "little", "work",
	"know", "place", "year", "live", "me", "back", "give", "most", "very", "after", "thing",
	"our", "just", "name", "good", "sentence", "man", "think", "say", "great", "where", "help",
	"hello", "thanks", "thank", "regards", "best", "kind", "dear", "please", "sincerely",
	"attached", "attachment", "forwarded", "following", "below", "above", "here", "sent",
	"meeting", "meetings", "schedule", "scheduled", "calendar", "invite", "confirm", "confirmed",
	"quote", "quotation", "invoice", "invoices", "billing", "payment", "payments", "paid",
	"balance", "account", "accounts", "due", "overdue", "receipt", "refund", "credit",
	"customer", "customers", "client", "clients", "vendor", "supplier", "partner", "partners",
	"contract", "contracts", "agreement", "proposal", "proposals", "report", "reports",
	"summary", "document", "documents", "file", "files", "folder", "version", "draft", "final",
	"project", "projects", "task", "tasks", "pending", "urgent", "important", "priority",
	"deadline", "delivery", "delivered", "progress", "update", "updates", "updated", "status",
	"team", "teams", "department", "management", "manager", "director", "office", "company",
	"business", "sales", "selling", "purchase", "order", "orders", "shipping", "shipment",
	"tracking", "warehouse", "inventory", "stock", "logistics", "freight", "carrier",
	"price", "pricing", "cost", "costs", "discount", "tax", "taxes", "total", "subtotal",
	"amount", "currency", "dollars", "euros", "bank", "transfer", "wire", "check", "cash",
	"card", "accounting", "financial", "finance", "budget", "forecast", "revenue", "expense",
	"audit", "compliance", "legal", "lawyer", "counsel", "clause", "terms", "conditions",
	"signature", "signed", "support", "service", "services", "system", "systems", "software",
	"application", "platform", "user", "users", "access", "password", "credentials", "permission",
	"error", "errors", "issue", "issues", "bug", "failure", "incident", "ticket", "request",
	"response", "solution", "problem", "problems", "test", "testing", "development", "production",
	"server", "servers", "database", "network", "email", "message", "messages", "call", "phone",
	"contact", "available", "availability", "hours", "week", "weeks", "month", "months",
	"quarter", "annual", "monthly", "weekly", "daily", "today", "tomorrow", "yesterday",
	"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
	"january", "february", "march", "april", "may", "june", "july", "august", "september",
	"october", "november", "december",
	"need", "needed", "want", "should", "must", "let", "know", "review", "reviewed", "approve",
	"approved", "rejected", "change", "changes", "modify", "coordinate", "organize", "plan",
	"planning", "strategy", "goal", "goals", "target", "result", "results", "metric", "growth",
	"improvement", "quality", "efficiency", "productivity", "training", "course", "workshop",
	"event", "presentation", "marketing", "campaign", "advertising", "communication", "press",
	"social", "content", "staff", "resources", "employee", "employees", "salary", "payroll",
	"vacation", "leave", "absence", "attendance", "onboarding", "resignation", "insurance",
	"policy", "coverage", "claim", "risk", "risks", "installation", "maintenance", "repair",
	"material", "materials", "equipment", "tools", "supplies", "quantity", "unit", "units",
	"north", "south", "east", "west", "center", "zone", "region", "province", "city", "street",
	"avenue", "number", "floor", "code", "postal", "address", "attention", "notice", "reminder",
	"follow", "followup", "regarding", "concerning", "reference", "subject", "topic", "detail",
	"details", "information", "data", "figures", "numbers", "chart", "table", "list", "item",
	"items", "option", "options", "alternative", "recommendation", "decision", "approval",
}

// Proper-noun pools: names and companies. Mail is full of these and they are
// mid-frequency tokens — neither stopword-common nor Zipf-tail-rare — which is
// exactly the band where GIN posting lists get interesting.

var firstNames = []string{
	"Diego", "Maria", "Juan", "Ana", "Carlos", "Laura", "Jose", "Sofia", "Luis", "Valentina",
	"Miguel", "Camila", "Jorge", "Lucia", "Pedro", "Martina", "Pablo", "Elena", "Andres", "Paula",
	"Ricardo", "Gabriela", "Fernando", "Daniela", "Roberto", "Carolina", "Alberto", "Natalia",
	"Sergio", "Patricia", "Javier", "Veronica", "Alejandro", "Silvia", "Manuel", "Claudia",
	"Rafael", "Monica", "Eduardo", "Beatriz", "Oscar", "Cecilia", "Hugo", "Rocio", "Ramiro",
	"John", "Sarah", "Michael", "Emily", "David", "Jessica", "James", "Ashley", "Robert",
	"Amanda", "William", "Melissa", "Richard", "Nicole", "Thomas", "Elizabeth", "Daniel",
	"Rebecca", "Matthew", "Laura", "Christopher", "Michelle", "Andrew", "Kimberly", "Joseph",
	"Stephanie", "Ryan", "Rachel", "Brian", "Katherine", "Kevin", "Samantha", "Eric", "Megan",
}

var lastNames = []string{
	"Gonzalez", "Rodriguez", "Gomez", "Fernandez", "Lopez", "Diaz", "Martinez", "Perez",
	"Garcia", "Sanchez", "Romero", "Sosa", "Torres", "Alvarez", "Ruiz", "Ramirez", "Flores",
	"Acosta", "Benitez", "Medina", "Suarez", "Herrera", "Aguirre", "Pereyra", "Gutierrez",
	"Molina", "Silva", "Castro", "Rojas", "Ortiz", "Nunez", "Luna", "Juarez", "Cabrera",
	"Smith", "Johnson", "Williams", "Brown", "Jones", "Miller", "Davis", "Wilson", "Anderson",
	"Taylor", "Thomas", "Moore", "Jackson", "Martin", "Lee", "Thompson", "White", "Harris",
	"Clark", "Lewis", "Walker", "Hall", "Young", "Allen", "King", "Wright", "Scott", "Green",
}

var companyStems = []string{
	"Andina", "Austral", "Boreal", "Cardinal", "Celeste", "Cordillera", "Delta", "Estuario",
	"Frontera", "Galena", "Horizonte", "Ibera", "Jacaranda", "Kalmar", "Lumina", "Meridiano",
	"Nova", "Ombu", "Pampa", "Quebracho", "Riachuelo", "Sierra", "Talampaya", "Umbral",
	"Ventana", "Wanda", "Xilema", "Yatay", "Zonda", "Alamo", "Bahia", "Caldera", "Dique",
	"Northwind", "Contoso", "Fabrikam", "Litware", "Proseware", "Adventure", "Tailspin",
	"Wingtip", "Woodgrove", "Coho", "Fourth", "Graphic", "Humongous", "Lucerne", "Margie",
	"Trey", "Blue", "Alpine", "Summit", "Beacon", "Harbor", "Ironwood", "Keystone", "Lakeside",
}

var companySuffixes = []string{
	"SA", "SRL", "SAS", "Group", "Holdings", "Partners", "Solutions", "Systems", "Logistics",
	"Consulting", "Industries", "Technologies", "Services", "Global", "International", "Labs",
}

var tlds = []string{"com", "com.ar", "net", "org", "io", "co", "cl", "mx", "es", "com.br"}
