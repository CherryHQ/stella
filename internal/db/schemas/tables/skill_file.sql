-- Holds every file for a skill, including the main "SKILL.md".
CREATE TABLE skill_file (
    skill_id TEXT NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
    path     TEXT NOT NULL,
    content  TEXT NOT NULL,
    PRIMARY KEY (skill_id, path)
);
