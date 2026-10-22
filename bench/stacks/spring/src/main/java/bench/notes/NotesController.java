package bench.notes;

import java.util.List;
import java.util.Map;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RestController;

// The notes API as a typical Spring Boot app: Spring MVC and Spring Data JPA on Postgres.
@RestController
public class NotesController {
    private final NoteRepository notes;
    private final JdbcTemplate jdbc;

    NotesController(NoteRepository notes, JdbcTemplate jdbc) {
        this.notes = notes;
        this.jdbc = jdbc;
    }

    @GetMapping("/healthz")
    public String healthz() {
        jdbc.queryForObject("SELECT 1", Integer.class);
        return "ok";
    }

    @GetMapping("/api/notes")
    public List<Note> list() {
        return notes.findTop100ByOrderByIdDesc();
    }

    @PostMapping("/api/notes")
    public ResponseEntity<?> create(@RequestBody Map<String, Object> in) {
        String body = in.get("body") instanceof String s ? s.strip() : "";
        if (body.isEmpty()) {
            return ResponseEntity.badRequest().body("body is required");
        }
        return ResponseEntity.status(HttpStatus.CREATED).body(notes.save(new Note(body)));
    }
}
